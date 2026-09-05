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
		if served[rt.Group] {
			servedRoutes = append(servedRoutes, rt)
		}
	}

	extractorByKey := r.resolveExtractors(in, plan)
	r.resolveUses(in, plan, served, servedRoutes)

	for _, rt := range servedRoutes {
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
		if problem := middlewareMethodProblem(d.TypeT); problem != "" {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf("servo: %s must have a method Middleware(next http.Handler) http.Handler to be attached with servo.Use — %s", d.Type, problem)})
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
	if res.Len() != 2 || !types.Identical(res.At(1).Type(), errorType) {
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
