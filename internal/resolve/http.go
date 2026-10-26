package resolve

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/route"
)

// HTTPInput is the resolver's slice of a servo.HTTP(...) declaration: the
// marker's position, the module's scanned routes (all of them — filtering
// by served group happens here), the declared groups and middleware, the
// module's extractors, and the key for the *servo.HTTPConfig node.
type HTTPInput struct {
	Pos        token.Position
	Routes     []*route.Route
	Groups     []load.GroupDecl
	Uses       []load.UseDecl
	Extracts   []load.ExtractDecl
	ConfigKey  graph.Key
	ConfigType types.Type
}

// HTTPPlan is the resolved half of the emitted HTTP servers: the config
// node, the served groups, and per route the classified handler arguments.
// The servers themselves are emitted machinery, not nodes — their
// requirements enter the graph here as root-like entry points.
type HTTPPlan struct {
	Pos    token.Position
	Config *Node
	// Groups is the served non-default group names, sorted. The default
	// group is always served and has no entry here.
	Groups []string
	// Routes keeps the scan's order and contains only served groups'
	// routes.
	Routes []*HTTPRoute
	// Uses keeps declaration order — it is the wrap order, outermost
	// first.
	Uses []*HTTPUse
	// Extractors keeps declaration order.
	Extractors []*HTTPExtractor
}

// HTTPRoute pairs one scanned route with its classified handler arguments,
// parallel to Route.Deps.
type HTTPRoute struct {
	Route *route.Route
	Args  []HTTPArg
}

// HTTPArg is one handler parameter after ctx (and the request struct):
// either a graph singleton or a per-request extracted value.
type HTTPArg struct {
	Node      *Node          // non-nil for a graph dependency
	Extractor *HTTPExtractor // non-nil for an extracted parameter
}

// HTTPUse is one resolved servo.Use attachment.
type HTTPUse struct {
	Node *Node
	Decl load.UseDecl
}

// HTTPExtractor is one resolved servo.Extract declaration: the extractor
// node and the type its Extract method produces.
type HTTPExtractor struct {
	Node         *Node
	Produces     graph.Key
	ProducesType types.Type
}

// HTTPRouteArgs renders one route's handler arguments for display: the
// graph key for dependencies, the produced type marked "(extracted)" for
// extractor parameters. Shared by `servo graph`'s converter and the emitted
// Graph() method so both surfaces agree.
func HTTPRouteArgs(hr *HTTPRoute) []string {
	var out []string
	for _, arg := range hr.Args {
		if arg.Extractor != nil {
			out = append(out, arg.Extractor.Produces.String()+" (extracted)")
			continue
		}
		out = append(out, arg.Node.Key.String())
	}
	return out
}

// HTTPUseScope renders what one servo.Use attachment wraps: "server",
// "group <name>", or "route <METHOD /pattern>" lists.
func HTTPUseScope(u *HTTPUse) string {
	switch {
	case len(u.Decl.Routes) > 0:
		var pats []string
		for _, sel := range u.Decl.Routes {
			pats = append(pats, sel.Pattern)
		}
		return "route " + strings.Join(pats, ", ")
	case len(u.Decl.Groups) > 0:
		return "group " + strings.Join(u.Decl.Groups, ", ")
	default:
		return "server"
	}
}

// resolveHTTP resolves the config key, the extractors, the middleware and
// every served handler dependency through the ordinary resolveKey
// recursion, so unresolved keys render with the same needed-by chains as
// any root's. The HTTP-specific rules live here because the consumer (the
// emitted server) is not a node checkScopeEdges could see: dependencies,
// middleware and extractors must all be singletons, and Use selectors must
// name things that exist.
func (r *resolver) resolveHTTP(in *HTTPInput) *HTTPPlan {
	if in == nil {
		return nil
	}
	plan := &HTTPPlan{Pos: in.Pos}

	chain := []chainEntry{{Key: in.ConfigKey, Label: "HTTP server (servo.HTTP())", Pos: in.Pos}}
	if node, ok := r.resolveKey(in.ConfigKey, in.ConfigType, chain, in.Pos); ok {
		plan.Config = node
		if _, seen := r.rootPos[node]; !seen {
			r.rootPos[node] = in.Pos
		}
	}

	// served maps both spellings of the default group; Route.Group uses "".
	served := map[string]bool{"": true, "default": true}
	for _, g := range in.Groups {
		served[g.Name] = true
		plan.Groups = append(plan.Groups, g.Name)
	}
	sort.Strings(plan.Groups)

	var servedRoutes []*route.Route
	for _, rt := range in.Routes {
		if served[rt.Group] && r.servableHandler(rt) {
			servedRoutes = append(servedRoutes, rt)
		}
	}

	extractorByKey := r.resolveExtractors(in, plan)
	r.resolveUses(in, plan, served, servedRoutes)

	for _, rt := range servedRoutes {
		// A request struct is decoded from the request, so a type the graph
		// provides must never be one: the handler would get what the client
		// sent in place of the value the graph builds. The same rule as an
		// extractor's "constructed once or extracted per request, never
		// both", for the one position the scanner classifies by tags alone.
		if rt.Req != nil {
			if by := r.requestStructProvider(rt.Req.Type, extractorByKey); by != "" {
				r.diags = append(r.diags, r.providedRequestStructDiagnostic(rt, by))
				continue
			}
		}
		hr := &HTTPRoute{Route: rt}
		label := fmt.Sprintf("handler %s (%s %s)", rt.Name, rt.Method, rt.Pattern)
		rchain := []chainEntry{{Label: label, Pos: rt.Pos}}
		for i, depKey := range rt.Deps {
			if ex, ok := extractorByKey[depKey]; ok {
				hr.Args = append(hr.Args, HTTPArg{Extractor: ex})
				continue
			}
			before := len(r.diags)
			node, ok := r.resolveKey(depKey, rt.DepTypes[i], rchain, in.Pos)
			if !ok {
				r.maybeRequestStructHint(before, rt, i)
				continue
			}
			if node.Kind == NodeContext {
				r.diags = append(r.diags, r.handlerContextDiagnostic(rt))
				continue
			}
			if node.Scoped() && node.Kind == NodeProvider {
				r.diags = append(r.diags, r.scopedHandlerDepDiagnostic(rt, node))
				continue
			}
			hr.Args = append(hr.Args, HTTPArg{Node: node})
			if _, seen := r.rootPos[node]; !seen {
				r.rootPos[node] = rt.Pos
			}
		}
		plan.Routes = append(plan.Routes, hr)
	}
	return plan
}

// servableHandler reports whether this injector can call rt's handler. A
// handler declared in a package main is reachable only from that package's
// own injector — nothing can import a main package — so for every other
// injector the route is simply not one of its to serve, the way a route
// in an undeclared group is not. Whether some injector serves such a
// handler at all is a module-wide question, answered by the pipeline's
// checkMainPackageRoutes; refusing it here would make a module with two
// HTTP binaries, each keeping its handlers beside its own spec, impossible.
func (r *resolver) servableHandler(rt *route.Route) bool {
	pkg := rt.Func.Pkg()
	return pkg == nil || pkg.Name() != "main" || pkg.Path() == r.injectorPath
}

// resolveExtractors resolves each servo.Extract declaration, validates its
// Extract method's shape, and returns the produced-type index used to
// classify handler parameters.
func (r *resolver) resolveExtractors(in *HTTPInput, plan *HTTPPlan) map[graph.Key]*HTTPExtractor {
	extractorByKey := map[graph.Key]*HTTPExtractor{}
	for _, d := range in.Extracts {
		chain := []chainEntry{{Key: d.Type, Label: "extractor " + d.Type.String() + " (servo.Extract)", Pos: d.Pos}}
		node, ok := r.resolveKey(d.Type, d.TypeT, chain, in.Pos)
		if !ok {
			continue
		}
		if node.Scoped() && node.Kind == NodeProvider {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s is scoped, but servo.Extract needs a singleton — the extractor is resolved once and its method called per request", d.Type)})
			continue
		}
		produces, problem := extractMethodResult(d.TypeT)
		if problem != "" {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s must have a method Extract(r *http.Request) (T, error) to be declared with servo.Extract — %s", d.Type, problem)})
			continue
		}
		key := graph.NewKey(produces, "")
		if prior, dup := extractorByKey[key]; dup {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: two extractors produce %s — %s and %s; a handler parameter type maps to exactly one extractor", key, prior.Node.Key, d.Type)})
			continue
		}
		if cands := r.byResult[key]; len(cands) > 0 {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s has both a provider (%s at %s) and an extractor (%s) — a type is either constructed once or extracted per request, never both", key, cands[0].Name, cands[0].Pos, d.Type)})
			continue
		}
		ex := &HTTPExtractor{Node: node, Produces: key, ProducesType: produces}
		extractorByKey[key] = ex
		plan.Extractors = append(plan.Extractors, ex)
		if _, seen := r.rootPos[node]; !seen {
			r.rootPos[node] = d.Pos
		}
	}
	return extractorByKey
}

// resolveUses resolves each servo.Use attachment, validates the Middleware
// method's shape, and checks every selector against what this injector
// actually serves — a selector naming nothing must fail generate, not
// silently wrap nothing.
func (r *resolver) resolveUses(in *HTTPInput, plan *HTTPPlan, served map[string]bool, servedRoutes []*route.Route) {
	for _, d := range in.Uses {
		chain := []chainEntry{{Key: d.Type, Label: "middleware " + d.Type.String() + " (servo.Use)", Pos: d.Pos}}
		node, ok := r.resolveKey(d.Type, d.TypeT, chain, in.Pos)
		if !ok {
			continue
		}
		if node.Scoped() && node.Kind == NodeProvider {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s is scoped, but servo.Use wraps a singleton server that would capture it — middleware must be singletons", d.Type)})
			continue
		}
		problem := middlewareMethodProblem(d.TypeT)
		if sig, ok := types.Unalias(d.TypeT).Underlying().(*types.Signature); ok && methodNamed(d.TypeT, "Middleware") == nil {
			// A plain middleware function — the shape otelhttp, chi and
			// gorilla/handlers ship — is attached as itself: the emitted
			// chain calls the node instead of a Middleware method on it,
			// so none of them needs a wrapper type. A Middleware method,
			// where a func type declares one, stays the explicit form.
			problem = ""
			if sig.Variadic() || sig.Params().Len() != 1 || !isNetHTTPNamed(sig.Params().At(0).Type(), "Handler", false) ||
				sig.Results().Len() != 1 || !isNetHTTPNamed(sig.Results().At(0).Type(), "Handler", false) {
				problem = "it is a function, but not a func(http.Handler) http.Handler"
			}
		}
		if problem != "" {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s must have a method Middleware(next http.Handler) http.Handler, or be a func(http.Handler) http.Handler, to be attached with servo.Use — %s", d.Type, problem)})
			continue
		}
		bad := false
		for _, g := range d.Groups {
			if !served[g] {
				names := "default"
				if len(plan.Groups) > 0 {
					names = "default, " + strings.Join(plan.Groups, ", ")
				}
				r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: servo.Use[%s] selects group %q, but this injector serves only: %s", d.Type, g, names)})
				bad = true
			}
		}
		for _, sel := range d.Routes {
			found := false
			for _, rt := range servedRoutes {
				if rt.Method+" "+rt.Pattern == sel.Pattern {
					found = true
					break
				}
			}
			if !found {
				r.diags = append(r.diags, Diagnostic{Pos: sel.Pos, Message: fmt.Sprintf("servo: servo.Use[%s]'s selector %q matches no served route", d.Type, sel.Pattern)})
				bad = true
			}
		}
		if bad {
			continue
		}
		plan.Uses = append(plan.Uses, &HTTPUse{Node: node, Decl: d})
		if _, seen := r.rootPos[node]; !seen {
			r.rootPos[node] = d.Pos
		}
	}
}

// extractMethodResult validates T's Extract method and returns the type it
// produces. The method is found by name — its result varies per type, so
// no single interface shape could match it (the same trade ScopeKey made).
func extractMethodResult(t types.Type) (types.Type, string) {
	fn := methodNamed(t, "Extract")
	if fn == nil {
		return nil, "it has no Extract method"
	}
	sig := fn.Type().(*types.Signature)
	if sig.Params().Len() != 1 || !isNetHTTPNamed(sig.Params().At(0).Type(), "Request", true) {
		return nil, "its parameter list must be exactly (r *http.Request)"
	}
	res := sig.Results()
	if res.Len() != 2 || !graph.IsErrorType(res.At(1).Type()) {
		return nil, "its results must be exactly (T, error)"
	}
	return res.At(0).Type(), ""
}

// middlewareMethodProblem validates T's Middleware method:
// Middleware(next http.Handler) http.Handler, exactly.
func middlewareMethodProblem(t types.Type) string {
	fn := methodNamed(t, "Middleware")
	if fn == nil {
		return "it has no Middleware method"
	}
	sig := fn.Type().(*types.Signature)
	if sig.Params().Len() != 1 || !isNetHTTPNamed(sig.Params().At(0).Type(), "Handler", false) {
		return "its parameter list must be exactly (next http.Handler)"
	}
	if sig.Results().Len() != 1 || !isNetHTTPNamed(sig.Results().At(0).Type(), "Handler", false) {
		return "its result must be exactly http.Handler"
	}
	return ""
}

func methodNamed(t types.Type, name string) *types.Func {
	obj, _, _ := types.LookupFieldOrMethod(t, true, nil, name)
	fn, _ := obj.(*types.Func)
	return fn
}

// isNetHTTPNamed reports whether t is net/http's named type (or a pointer
// to it, when ptr is set) — identity by package path and name, so the
// check works without holding the net/http package itself.
func isNetHTTPNamed(t types.Type, name string, ptr bool) bool {
	u := types.Unalias(t)
	if ptr {
		p, ok := u.(*types.Pointer)
		if !ok {
			return false
		}
		u = types.Unalias(p.Elem())
	}
	named, ok := u.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == name
}

// maybeRequestStructHint extends the diagnostic a failed dependency just
// produced when the shape suggests the real mistake was elsewhere: the
// handler's second parameter is a pointer to a struct with no binding
// tags, so the scanner classified it as a dependency, and "no provider"
// alone sends the user hunting for a constructor they never meant to
// write. Appended only to a diagnostic added by this very call — a
// dependency that failed earlier (failedKey) was already hinted once.
func (r *resolver) maybeRequestStructHint(before int, rt *route.Route, depIdx int) {
	if len(r.diags) == before {
		return
	}
	if rt.Req != nil || depIdx != 0 {
		return // not in the request-struct position
	}
	ptr, ok := types.Unalias(rt.DepTypes[depIdx]).(*types.Pointer)
	if !ok {
		return
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	if !ok {
		return
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return
	}
	last := &r.diags[len(r.diags)-1]
	last.Message += fmt.Sprintf("\n  If %s was meant as the request struct rather than a dependency, give its\n  fields binding tags (path/query/header/form/json) — only the second\n  parameter can be the request struct, and the tags are what mark it as one.\n", graph.TypeString(named))
}

// requestStructProvider says how the graph provides t — a handler's
// request-struct type — or "" when it does not. Provided means anything a
// dependency in that position would have resolved to: a constructor, a
// servo.Value, a scope, an extractor, or a //servo:config type, whose node
// is the struct value the pointer points at.
func (r *resolver) requestStructProvider(t types.Type, extractorByKey map[graph.Key]*HTTPExtractor) string {
	key := graph.NewKey(t, "")
	if v, ok := r.suppliedByKey[key]; ok {
		return fmt.Sprintf("servo.Value[%s]() supplies it (%s)", key, v.SuppliedPos)
	}
	if r.declaredScope[key] {
		return "a servo.Scoped declaration makes it a scoped instance"
	}
	if ex, ok := extractorByKey[key]; ok {
		return fmt.Sprintf("the extractor %s produces it per request", ex.Node.Key)
	}
	if cands := r.byResult[key]; len(cands) > 0 {
		c := r.sortedCandidates(cands)[0]
		return fmt.Sprintf("%s constructs it (%s)", c.Name, c.Pos)
	}
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		if d, ok := r.configByKey[graph.NewKey(ptr.Elem(), "")]; ok {
			return fmt.Sprintf("it points at %s, which a //servo:%s directive loads (%s)", d.Key, graph.ConfigDirective, d.Pos)
		}
	}
	return ""
}

// providedRequestStructDiagnostic refuses a request struct the graph also
// provides. The second parameter is decoded from the request whenever it
// points to a struct with any binding or json tag, so an injected
// *settings.Settings in that position was silently replaced by the body the
// client sent — {"admin":true} included — while the same type in position
// three got the singleton.
func (r *resolver) providedRequestStructDiagnostic(rt *route.Route, by string) Diagnostic {
	typ := graph.TypeString(rt.Req.Type)
	var b strings.Builder
	fmt.Fprintf(&b, "servo: handler %s (%s %s) takes %s as its request struct, but the graph provides that type: %s\n", rt.Name, rt.Method, rt.Pattern, typ, by)
	b.WriteString("\n  A handler's second parameter is decoded from the request whenever it points to\n")
	b.WriteString("  a struct with path, query, header, form or json tags, so this handler would get\n")
	fmt.Fprintf(&b, "  whatever the client sent in place of the %s the graph builds.\n", typ)
	b.WriteString("  Give the route a request type of its own and take the provided one after it:\n\n")
	fmt.Fprintf(&b, "\tfunc %s(ctx context.Context, req *%sRequest, %s) ...\n\n", rt.Func.Name(), rt.Func.Name(), localParam(rt.Req.Type))
	b.WriteString("  — or, if the handler reads nothing from the request, put any other dependency\n  second so that this one is not.\n")
	return Diagnostic{Pos: rt.Pos, Message: b.String()}
}

// localParam renders a parameter declaration for t as a handler in t's own
// package would write it: settings *Settings.
func localParam(t types.Type) string {
	name := "v"
	if named := typeNameOf(t); named != nil {
		name = strings.ToLower(named.Obj().Name()[:1]) + named.Obj().Name()[1:]
	}
	return name + " " + localTypeString(t)
}

// handlerContextDiagnostic refuses a context.Context among a handler's
// dependencies. The first parameter is the request's context; a later one
// would be resolved once, when the server is built, as the ctx New was
// called with — never the request's.
func (r *resolver) handlerContextDiagnostic(rt *route.Route) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: handler %s (%s %s) takes a second context.Context\n", rt.Name, rt.Method, rt.Pattern)
	b.WriteString("\n  The first parameter is the request's context. Every parameter after the request\n")
	b.WriteString("  struct is resolved once, when the server is built, so a context.Context there would\n")
	b.WriteString("  be the ctx New was called with, not this request's. Use the first parameter.\n")
	return Diagnostic{Pos: rt.Pos, Message: b.String()}
}

// scopedHandlerDepDiagnostic is the widening rule with the emitted HTTP
// server as the capturing singleton: the server's dependencies are
// resolved once at construction, so a scoped parameter would pin one key's
// instance for every request.
func (r *resolver) scopedHandlerDepDiagnostic(rt *route.Route, scoped *Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is scoped, but handler %s (%s %s) receives it from the HTTP server, a singleton that would capture it\n\n",
		scoped.Key.String(), rt.Name, rt.Method, rt.Pattern)
	fmt.Fprintf(&b, "  The server resolves a handler's dependencies once, at construction, so every\n")
	fmt.Fprintf(&b, "  request would share whichever %s happened to be built first,\n", scoped.Key.String())
	fmt.Fprintf(&b, "  whatever key the request presents. Nothing about the running program would say so.\n\n")
	if iface := r.accessorIfaceFor(scoped); iface != "" {
		fmt.Fprintf(&b, "  Depend on the accessor instead: change the handler's parameter from %s\n  to %s, and call Acquire(ctx) per request.\n", scoped.Key.String(), iface)
	} else {
		fmt.Fprintf(&b, "  Depend on the scope's accessor interface instead of on %s directly,\n  and call Acquire(ctx) per request.\n", scoped.Key.String())
	}
	return Diagnostic{Pos: rt.Pos, Message: b.String()}
}
