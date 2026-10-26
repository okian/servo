package emit

import (
	"fmt"
	"go/types"
	"path"
	"slices"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/internal/route"
)

// httpEmit is the resolved HTTP plan plus every identifier the generated
// servers need, allocated once up front like scopeEmit's — the servers are
// the same kind of synthetic machinery scopes are: emitted, not nodes.
type httpEmit struct {
	Plan *resolve.HTTPPlan

	// Groups holds one emit unit per served group: the default group
	// first, then the declared groups in sorted order.
	Groups []*httpGroupEmit

	// ServerUses are the selector-less servo.Use attachments (every
	// group), in declaration order; groupUses and routeUses hold the
	// selected ones. Order within each is declaration order — the wrap
	// order, outermost first.
	ServerUses []*resolve.HTTPUse
	groupUses  map[string][]*resolve.HTTPUse
	routeUses  map[*resolve.HTTPRoute][]*resolve.HTTPUse

	// Impl is the identifier generated code reaches servoimpl by: building,
	// serving and stopping a listener, the status contract and the error
	// envelope are the same for every server, so they live in the runtime
	// rather than in each generated file. URL is net/url's, set only when
	// a route binds a query parameter.
	Impl string
	URL  string

	// StopAll is the App method that stops every group's server at once
	// and StopOnce the App field guarding it, allocated only when there is
	// more than one group: Shutdown reaches the servers one stop method at
	// a time, and whichever comes first stops them all, so their
	// ShutdownDelays run side by side instead of end to end.
	StopAll  string
	StopOnce string
}

// httpGroupEmit is one group's emitted server.
type httpGroupEmit struct {
	Name       string // "" = default
	ServerType string // httpServer / httpTelemetryServer
	NewFunc    string // newHttpServer / ...
	Field      string // App field
	StopMethod string // stopHttpServer / ...
	Routes     []*httpRouteEmit
	// needsBody is whether any of THIS group's routes reads a request
	// body, which decides the group server's maxBody field.
	needsBody bool
}

type httpRouteEmit struct {
	R       *resolve.HTTPRoute
	Adapter string // method name on the group's server type
}

// httpServerReservedMembers is every field and method a group server type
// declares itself; adapters share the namespace (a Go field and method
// collide), so they are claimed before any adapter gets a name.
var httpServerReservedMembers = []string{
	"app", "lc", "srv", "ready", "maxBody", "cancel",
	"run", "stop",
	"s", "w", "r",
}

// httpReservedIdents are the parameters and locals emitted HTTP code names
// outright — same rationale as scopeReservedIdents: a user package taking
// one of these identifiers would shadow it exactly where generated code
// qualifies a type with it.
var httpReservedIdents = []string{
	"w", "r", "req", "res", "raw", "v", "q", "err",
	"mux", "lc", "h", "handler", "s", "wg",
}

// stopName is the NodeResult name a group's server reports under.
func (g *httpGroupEmit) stopName() string {
	if g.Name == "" {
		return "http"
	}
	return "http:" + g.Name
}

// planHTTP allocates every HTTP identifier and registers the imports the
// emitted servers hard-code. Runs after planScopes so scope declarations
// keep the names they have always gotten.
func (e *emitter) planHTTP() {
	plan := e.resolved.HTTP
	if plan == nil {
		return
	}
	e.imports.Add("net/http", "http")
	e.imports.Add("sync", "sync")
	e.imports.Add("sync/atomic", "atomic")
	e.imports.Reserve(httpReservedIdents...)

	h := &httpEmit{
		Plan:      plan,
		groupUses: map[string][]*resolve.HTTPUse{},
		routeUses: map[*resolve.HTTPRoute][]*resolve.HTTPUse{},
	}
	// The same expression the lifecycle emitter registers servoimpl with:
	// Add is idempotent, so whichever asks first, the alias is one.
	h.Impl = e.imports.Add(path.Join(path.Dir(graph.ServoPackagePath), "servoimpl"), "servoimpl")

	// The rest of the standard library the adapters hard-code, registered
	// only when a route needs it — a file that imports encoding/json
	// without decoding a body does not compile — and here rather than at
	// first use, so each wins its identifier against a user package of
	// the same name before any adapter qualifies a type.
	var jsonBody, form, parsed, query bool
	for _, hr := range plan.Routes {
		req := hr.Route.Req
		if req == nil {
			continue
		}
		jsonBody = jsonBody || req.HasBody
		form = form || req.HasForm
		for _, f := range req.Fields {
			query = query || f.Kind == route.BindQuery
			if b, ok := types.Unalias(f.Type).(*types.Basic); ok && b.Kind() != types.String && f.Kind != route.BindBody {
				parsed = true
			}
		}
	}
	if jsonBody {
		e.imports.Add("encoding/json", "json")
	}
	if form {
		e.imports.Add("errors", "errors")
	}
	if parsed {
		e.imports.Add("strconv", "strconv")
	}
	if query {
		h.URL = e.imports.Add("net/url", "url")
	}

	for _, u := range plan.Uses {
		switch {
		case len(u.Decl.Routes) > 0:
			for _, sel := range u.Decl.Routes {
				for _, hr := range plan.Routes {
					if hr.Route.Method+" "+hr.Route.Pattern == sel.Pattern {
						h.routeUses[hr] = append(h.routeUses[hr], u)
					}
				}
			}
		case len(u.Decl.Groups) > 0:
			for _, g := range u.Decl.Groups {
				if g == "default" {
					g = ""
				}
				h.groupUses[g] = append(h.groupUses[g], u)
			}
		default:
			h.ServerUses = append(h.ServerUses, u)
		}
	}

	// The default group always exists (it always has a listener to
	// describe, even with zero routes it would just 404); declared groups
	// follow in sorted order — Plan.Groups is already sorted.
	names := append([]string{""}, plan.Groups...)
	for _, name := range names {
		base := "httpServer"
		if name != "" {
			base = "http" + exportedIdent(name) + "Server"
		}
		g := &httpGroupEmit{Name: name}
		g.ServerType = e.types.AllocateName(e.testPrefixed(base))
		g.NewFunc = e.types.AllocateName("new" + capitalize(e.testPrefixed(base)))
		g.Field = allocateAppField(e.names, base)
		g.StopMethod = "stop" + capitalize(g.Field)

		adapterNames := NewNameAllocator()
		for _, reserved := range httpServerReservedMembers {
			adapterNames.AllocateName(reserved)
		}
		count := map[string]int{}
		for _, hr := range plan.Routes {
			if hr.Route.Group == name {
				count[hr.Route.Func.Name()]++
			}
		}
		for _, hr := range plan.Routes {
			if hr.Route.Group != name {
				continue
			}
			adapterBase := hr.Route.Func.Name()
			if count[adapterBase] > 1 && hr.Route.Func.Pkg() != nil {
				adapterBase = capitalize(hr.Route.Func.Pkg().Name()) + capitalize(adapterBase)
			}
			g.Routes = append(g.Routes, &httpRouteEmit{R: hr, Adapter: adapterNames.AllocateName("handle" + capitalize(adapterBase))})
			if req := hr.Route.Req; req != nil && (req.HasBody || req.HasForm) {
				g.needsBody = true
			}
		}
		h.Groups = append(h.Groups, g)
	}
	// After every group's field has claimed its derived stop names, so a
	// node or group whose own stop method happens to be called
	// stopHttpServers is never shadowed by this one.
	if len(h.Groups) > 1 {
		h.StopOnce = e.names.AllocateName("httpServersStopOnce")
		h.StopAll = e.names.AllocateName("stopHttpServers")
	}
	e.http = h
}

// exportedIdent turns a group name (grammar [A-Za-z0-9_-]+) into an
// exported identifier fragment: "back-office" -> "BackOffice".
func exportedIdent(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(capitalize(p))
	}
	if b.Len() == 0 {
		return "Group"
	}
	return b.String()
}

// httpAppFields renders each group server's App field and its stop
// bookkeeping: the same triple every stoppable node gets when there is one
// group, and one sync.Once shared by every group when there are several —
// the servers stop together, so they are guarded together.
func (e *emitter) httpAppFields() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	for _, g := range e.http.Groups {
		fmt.Fprintf(&b, "\t%s *%s\n", g.Field, g.ServerType)
		if e.http.StopOnce == "" {
			fmt.Fprintf(&b, "\t%sStopOnce sync.Once\n", g.Field)
		}
		fmt.Fprintf(&b, "\t%sStopResult %s.NodeResult\n", g.Field, e.servoAlias)
	}
	if e.http.StopOnce != "" {
		fmt.Fprintf(&b, "\t%s sync.Once\n", e.http.StopOnce)
	}
	return b.String()
}

// httpSetup wires the servers once every node is constructed and before
// any Init runs. They are pure wiring — a middleware's Middleware method is
// called here under the guarantee every constructor gets, that its
// dependencies' constructors have returned and their Inits have not — and
// the listeners bind in run(). Sitting ahead of Init is what makes a named
// group missing its HTTPConfig.Groups entry fail before anything expensive
// has started; the unwind is the same Shutdown-and-join an Init failure
// takes, on a context the signal cannot cancel.
func (e *emitter) httpSetup() string {
	if e.http == nil {
		return ""
	}
	e.imports.Add("errors", "errors")
	var b strings.Builder
	for _, g := range e.http.Groups {
		fmt.Fprintf(&b, "\t%s, err := %s(a)\n", g.Field, g.NewFunc)
		b.WriteString("\tif err != nil {\n")
		b.WriteString(e.rollbackReturn())
		b.WriteString("\t}\n")
		fmt.Fprintf(&b, "\ta.%s = %s\n\n", g.Field, g.Field)
	}
	return b.String()
}

// httpHeader documents the groups and the routing table in the generated
// file's header, beside the resolved graph and the scopes.
func (e *emitter) httpHeader() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "//\n// http (servo.HTTP() at %s):\n", e.posString(e.http.Plan.Pos))
	fmt.Fprintf(&b, "//   config: %s\n", e.http.Plan.Config.Key.String())
	if len(e.http.Plan.Groups) > 0 {
		fmt.Fprintf(&b, "//   groups: %s\n", strings.Join(e.http.Plan.Groups, ", "))
	}
	for _, u := range e.http.Plan.Uses {
		scope := "every group"
		switch {
		case len(u.Decl.Routes) > 0:
			var pats []string
			for _, sel := range u.Decl.Routes {
				pats = append(pats, sel.Pattern)
			}
			scope = strings.Join(pats, ", ")
		case len(u.Decl.Groups) > 0:
			scope = "group " + strings.Join(u.Decl.Groups, ", ")
		}
		fmt.Fprintf(&b, "//   use: %s -> %s\n", u.Node.Key.String(), scope)
	}
	for _, ex := range e.http.Plan.Extractors {
		fmt.Fprintf(&b, "//   extract: %s -> %s\n", ex.Node.Key.String(), ex.Produces.String())
	}
	for _, g := range e.http.Groups {
		for _, re := range g.Routes {
			rt := re.R.Route
			deps := "none"
			var names []string
			for _, arg := range re.R.Args {
				if arg.Node != nil {
					names = append(names, arg.Node.Key.String())
				} else {
					names = append(names, arg.Extractor.Produces.String()+" (extracted)")
				}
			}
			if len(names) > 0 {
				deps = strings.Join(names, ", ")
			}
			groupNote := ""
			if g.Name != "" {
				groupNote = " [" + g.Name + "]"
			}
			fmt.Fprintf(&b, "//   %s %s%s -> %s (%s)\n//         args: %s\n", rt.Method, rt.Pattern, groupNote, rt.Name, e.posString(rt.Pos), deps)
		}
	}
	return b.String()
}

// httpDecls renders every group's server type, constructor, adapters, and
// run and stop methods. What they share — writing a response, the status
// contract, serving and draining a listener — is servoimpl's, so nothing
// here is package-level.
func (e *emitter) httpDecls() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	for _, g := range e.http.Groups {
		e.writeHTTPGroup(&b, g)
	}
	return b.String()
}

// writeHTTPGroup renders one group's server: struct, constructor with the
// middleware chain, adapters, and run and stop methods.
func (e *emitter) writeHTTPGroup(b *strings.Builder, g *httpGroupEmit) {
	h := e.http
	cfgField := e.varName[h.Plan.Config.Key]
	label := "the default group"
	if g.Name != "" {
		label = fmt.Sprintf("group %q", g.Name)
	}

	fmt.Fprintf(b, "// %s serves %s's //servo: routes. It is generated\n", g.ServerType, label)
	b.WriteString("// machinery, not a graph node: its dependencies are ordinary singletons\n")
	fmt.Fprintf(b, "// on the %s, resolved once at construction.\n", e.appType())
	fmt.Fprintf(b, "type %s struct {\n", g.ServerType)
	fmt.Fprintf(b, "\tapp *%s\n", e.appType())
	fmt.Fprintf(b, "\tlc  %s.HTTPListener\n", e.servoAlias)
	b.WriteString("\tsrv *http.Server\n")
	if g.needsBody {
		b.WriteString("\tmaxBody int64\n")
	}
	b.WriteString("\t// cancel ends the context every request's context descends from —\n")
	b.WriteString("\t// the stop path's last resort when a drain outlives its deadline.\n")
	b.WriteString("\tcancel context.CancelCauseFunc\n")
	b.WriteString("\t// ready flips once the listener is bound, which is the precise moment\n")
	fmt.Fprintf(b, "\t// Ready starts reporting ok for the %q node.\n", g.stopName())
	b.WriteString("\tready atomic.Bool\n")
	b.WriteString("}\n\n")

	// servoimpl.Listener reads the flat HTTPConfig fields for the default
	// group and fails New, naming the group, when a declared group has no
	// Groups entry; servoimpl.NewServer loads a TLS key pair here too, so
	// a bad certificate fails New rather than Run.
	fmt.Fprintf(b, "func %s(a *%s) (*%s, error) {\n", g.NewFunc, e.appType(), g.ServerType)
	fmt.Fprintf(b, "\tlc, err := %s.Listener(a.%s, %q)\n", h.Impl, cfgField, g.Name)
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	fmt.Fprintf(b, "\ts := &%s{app: a, lc: lc}\n", g.ServerType)
	if g.needsBody {
		fmt.Fprintf(b, "\ts.maxBody = %s.BodyLimit(lc)\n", h.Impl)
	}
	b.WriteString("\tmux := http.NewServeMux()\n")
	for _, re := range g.Routes {
		pattern := re.R.Route.Method + " " + re.R.Route.Pattern
		uses := h.routeUses[re.R]
		if len(uses) == 0 {
			fmt.Fprintf(b, "\tmux.HandleFunc(%q, s.%s)\n", pattern, re.Adapter)
			continue
		}
		// Route-level wraps: applied in reverse declaration order so the
		// first-declared middleware is outermost.
		fmt.Fprintf(b, "\t{\n\t\tvar h http.Handler = http.HandlerFunc(s.%s)\n", re.Adapter)
		for _, u := range slices.Backward(uses) {
			fmt.Fprintf(b, "\t\th = %s\n", e.httpWrap(u, "h"))
		}
		fmt.Fprintf(b, "\t\tmux.Handle(%q, h)\n\t}\n", pattern)
	}

	wraps := append(append([]*resolve.HTTPUse{}, h.ServerUses...), h.groupUses[g.Name]...)
	handlerExpr := "mux"
	if len(wraps) > 0 {
		b.WriteString("\tvar handler http.Handler = mux\n")
		// Group wraps sit inside server wraps, and within each level the
		// first-declared is outermost — hence reverse application order.
		for _, u := range slices.Backward(wraps) {
			fmt.Fprintf(b, "\thandler = %s\n", e.httpWrap(u, "handler"))
		}
		handlerExpr = "handler"
	}
	fmt.Fprintf(b, "\ts.srv, s.cancel, err = %s.NewServer(%q, lc, %s)\n", h.Impl, g.stopName(), handlerExpr)
	b.WriteString("\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn s, nil\n}\n\n")

	for _, re := range g.Routes {
		e.writeHTTPAdapter(b, g, re)
	}

	e.writeHTTPRunStop(b, g)
}

// httpWrap renders one middleware application around inner: a call to the
// node's Middleware method, or — for a node that has none and is itself a
// func(http.Handler) http.Handler, the shape otelhttp, chi and
// gorilla/handlers ship — a call to the node. resolveUses has already
// checked that one of the two fits, on the same terms.
func (e *emitter) httpWrap(u *resolve.HTTPUse, inner string) string {
	node := "a." + e.varName[u.Node.Key]
	if httpMiddlewareIsFunc(u.Decl.TypeT) {
		return fmt.Sprintf("%s(%s)", node, inner)
	}
	return fmt.Sprintf("%s.Middleware(%s)", node, inner)
}

// httpMiddlewareIsFunc reports whether a servo.Use type attaches as a
// plain middleware function. A Middleware method wins when a func type
// declares one: it is the explicit form.
func httpMiddlewareIsFunc(t types.Type) bool {
	if obj, _, _ := types.LookupFieldOrMethod(t, true, nil, "Middleware"); obj != nil {
		if _, ok := obj.(*types.Func); ok {
			return false
		}
	}
	_, ok := types.Unalias(t).Underlying().(*types.Signature)
	return ok
}

// writeHTTPRunStop emits one group server's Run half and its stop, both
// servoimpl's: Serve binds explicitly, so ready flips at a precise moment,
// and returns when ctx ends or serving fails; StopServer drops readiness,
// serves out the listener's ShutdownDelay, drains for what is left of the
// shutdown deadline, and cancels and closes what is still running when
// that runs out.
func (e *emitter) writeHTTPRunStop(b *strings.Builder, g *httpGroupEmit) {
	h := e.http
	fmt.Fprintf(b, "func (s *%s) run(ctx context.Context) error {\n", g.ServerType)
	fmt.Fprintf(b, "\treturn %s.Serve(ctx, %q, s.srv, &s.ready)\n}\n\n", h.Impl, g.stopName())

	b.WriteString("// stop takes the server out of service. The nil receiver is the setup\n")
	b.WriteString("// failure path: a group missing its HTTPConfig.Groups entry unwinds\n")
	b.WriteString("// through Shutdown while its server, and every one after it, is still nil.\n")
	fmt.Fprintf(b, "func (s *%s) stop(ctx context.Context) %s.NodeResult {\n", g.ServerType, e.servoAlias)
	b.WriteString("\tif s == nil {\n")
	fmt.Fprintf(b, "\t\treturn %s.NodeResult{Name: %q, Status: %s.StatusOK}\n\t}\n", e.servoAlias, g.stopName(), e.servoAlias)
	fmt.Fprintf(b, "\treturn %s.StopServer(ctx, %q, s.srv, &s.ready, s.lc.ShutdownDelay, s.cancel)\n}\n\n", h.Impl, g.stopName())
}

// httpHandlerFunc emits the test seam: each group's fully wrapped handler,
// reachable without binding a listener, so httptest drives the emitted
// routing, middleware and adapters directly. Emitted only when the spec
// declares servo.HTTP(), which keeps no-HTTP output byte-identical.
func (e *emitter) httpHandlerFunc() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// HTTPHandler returns the named group's routed, middleware-wrapped handler\n")
	fmt.Fprintf(&b, "// — for httptest, which needs no bound listener. \"\" and \"default\" name the\n")
	fmt.Fprintf(&b, "// default group; unknown names return nil.\n")
	fmt.Fprintf(&b, "func (a *%s) HTTPHandler(group string) http.Handler {\n", e.appType())
	b.WriteString("\tswitch group {\n")
	for _, g := range e.http.Groups {
		if g.Name == "" {
			b.WriteString("\tcase \"\", \"default\":\n")
		} else {
			fmt.Fprintf(&b, "\tcase %q:\n", g.Name)
		}
		fmt.Fprintf(&b, "\t\treturn a.%s.srv.Handler\n", g.Field)
	}
	b.WriteString("\t}\n\treturn nil\n}\n\n")
	return b.String()
}

// httpStopMethod renders the App-level stops, sequenced first in Shutdown:
// the servers are the inbound edges, so they drain before anything they
// depend on. Each runs under sync.Once, so rollback and Shutdown can both
// reach it. With several groups the first call stops every group at once
// and the rest return what it recorded — one after another, each group's
// ShutdownDelay would add to the last.
func (e *emitter) httpStopMethod() string {
	if e.http == nil {
		return ""
	}
	h := e.http
	var b strings.Builder
	if h.StopAll == "" {
		g := h.Groups[0]
		fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.NodeResult {\n", e.appType(), g.StopMethod, e.servoAlias)
		fmt.Fprintf(&b, "\ta.%sStopOnce.Do(func() { a.%sStopResult = a.%s.stop(ctx) })\n", g.Field, g.Field, g.Field)
		fmt.Fprintf(&b, "\treturn a.%sStopResult\n}\n\n", g.Field)
		return b.String()
	}

	fmt.Fprintf(&b, "// %s stops every group's server together, the first time Shutdown\n", h.StopAll)
	b.WriteString("// reaches any of them: each serves out its own ShutdownDelay and drains\n")
	b.WriteString("// while the others do, so several groups' delays overlap instead of\n")
	b.WriteString("// adding up.\n")
	fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) {\n", e.appType(), h.StopAll)
	fmt.Fprintf(&b, "\ta.%s.Do(func() {\n", h.StopOnce)
	b.WriteString("\t\tvar wg sync.WaitGroup\n")
	for _, g := range h.Groups {
		fmt.Fprintf(&b, "\t\twg.Go(func() { a.%sStopResult = a.%s.stop(ctx) })\n", g.Field, g.Field)
	}
	b.WriteString("\t\twg.Wait()\n\t})\n}\n\n")
	for _, g := range h.Groups {
		fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.NodeResult {\n", e.appType(), g.StopMethod, e.servoAlias)
		fmt.Fprintf(&b, "\ta.%s(ctx)\n", h.StopAll)
		fmt.Fprintf(&b, "\treturn a.%sStopResult\n}\n\n", g.Field)
	}
	return b.String()
}

// writeHTTPAdapter emits one route's http.HandlerFunc: per-request
// extraction, typed decode, and the handler call with graph-resolved
// arguments, whose result servoimpl.Respond turns into the response.
func (e *emitter) writeHTTPAdapter(b *strings.Builder, g *httpGroupEmit, re *httpRouteEmit) {
	h := e.http
	rt := re.R.Route
	routeLabel := rt.Method + " " + rt.Pattern

	fmt.Fprintf(b, "func (s *%s) %s(w http.ResponseWriter, r *http.Request) {\n", g.ServerType, re.Adapter)

	// The qualifiers this adapter's body writes — the handler's package,
	// the request struct's — are registered before any local is named, so
	// the allocator below can see every identifier the body depends on.
	funcRef := e.httpFuncRef(rt.Func)
	reqType := ""
	if rt.Req != nil {
		reqType = e.qualifiedTypeString(rt.Req.Named)
	}

	// Extracted parameters come first, before a byte of the request is
	// decoded: an extractor is usually the credential check, so a rejected
	// request should cost nothing — the body is never read — and a
	// malformed body must not turn its 401 into a 400. Each failure goes
	// through the shared status mapping. The handler's arguments are still
	// assembled in parameter order, below.
	//
	// A local is named after the type it holds, and the body also names
	// packages: an extractor producing *user.User for a handler in package
	// user used to emit `user, err := ...Extract(r)` followed by
	// `user.Me(r.Context(), user)`, which the compiler reads as a method on
	// the local. Every import identifier is claimed ahead of the locals.
	extractedNames := NewNameAllocator()
	for _, reserved := range []string{"w", "r", "req", "q", "raw", "v", "res", "err", "s"} {
		extractedNames.AllocateName(reserved)
	}
	for _, ident := range e.imports.Idents() {
		extractedNames.AllocateName(ident)
	}
	depArgs := make([]string, 0, len(re.R.Args))
	for _, arg := range re.R.Args {
		if arg.Node != nil {
			depArgs = append(depArgs, "s.app."+e.httpDepField(arg.Node))
			continue
		}
		local := extractedNames.AllocateName(baseName(arg.Extractor.ProducesType))
		fmt.Fprintf(b, "\t%s, err := s.app.%s.Extract(r)\n", local, e.varName[arg.Extractor.Node.Key])
		b.WriteString("\tif err != nil {\n")
		fmt.Fprintf(b, "\t\t%s.WriteFailure(w, r, %q, %q, err)\n\t\treturn\n\t}\n", h.Impl, rt.Name, routeLabel)
		depArgs = append(depArgs, local)
	}

	args := []string{"r.Context()"}
	if rt.Req != nil {
		fmt.Fprintf(b, "\treq := &%s{}\n", reqType)
		if rt.Req.HasBody {
			b.WriteString("\t// The body decodes first, so the explicitly bound sources below\n")
			b.WriteString("\t// overwrite anything it could smuggle into a same-named field.\n")
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\tif err := json.NewDecoder(r.Body).Decode(req); err != nil {\n")
			fmt.Fprintf(b, "\t\t%s.BodyError(w, err, \"malformed request body\")\n\t\treturn\n\t}\n", h.Impl)
		}
		if rt.Req.HasForm {
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\t// ParseMultipartForm falls through to ParseForm on urlencoded\n")
			b.WriteString("\t// bodies, reporting ErrNotMultipart after already parsing them.\n")
			b.WriteString("\tif err := r.ParseMultipartForm(s.maxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {\n")
			fmt.Fprintf(b, "\t\t%s.BodyError(w, err, \"malformed form body\")\n\t\treturn\n\t}\n", h.Impl)
		}
		if httpNeedsQuery(rt) {
			b.WriteString("\t// Parsed here for the error r.URL.Query() throws away: a query\n")
			b.WriteString("\t// net/url refuses — a bad escape, a semicolon, too many parameters —\n")
			b.WriteString("\t// would otherwise bind every field to its zero value and answer 200.\n")
			fmt.Fprintf(b, "\tq, err := %s.ParseQuery(r.URL.RawQuery)\n", h.URL)
			b.WriteString("\tif err != nil {\n")
			fmt.Fprintf(b, "\t\t%s.WriteError(w, http.StatusBadRequest, \"malformed query string\")\n\t\treturn\n\t}\n", h.Impl)
		}
		for _, f := range rt.Req.Fields {
			e.writeHTTPFieldDecode(b, f)
		}
		args = append(args, "req")
	}
	args = append(args, depArgs...)

	fmt.Fprintf(b, "\tres, err := %s(%s)\n", funcRef, strings.Join(args, ", "))
	fmt.Fprintf(b, "\t%s.Respond(w, r, %q, %q, res, err)\n", h.Impl, rt.Name, routeLabel)
	b.WriteString("}\n\n")
}

func httpNeedsQuery(rt *route.Route) bool {
	for _, f := range rt.Req.Fields {
		if f.Kind == route.BindQuery {
			return true
		}
	}
	return false
}

// writeHTTPFieldDecode emits one bound field's assignment: strings assign
// directly, every other scalar gets its exact strconv parse with a 400
// naming the source on malformed input. Absent optional sources (query,
// header, form — and an empty trailing wildcard) leave the zero value.
func (e *emitter) writeHTTPFieldDecode(b *strings.Builder, f route.Field) {
	var raw string
	switch f.Kind {
	case route.BindPath:
		raw = fmt.Sprintf("r.PathValue(%q)", f.Param)
	case route.BindQuery:
		raw = fmt.Sprintf("q.Get(%q)", f.Param)
	case route.BindHeader:
		raw = fmt.Sprintf("r.Header.Get(%q)", f.Param)
	case route.BindForm:
		raw = fmt.Sprintf("r.PostForm.Get(%q)", f.Param)
	default:
		return // BindBody: encoding/json already populated it
	}

	basic := types.Unalias(f.Type).(*types.Basic)
	if basic.Kind() == types.String {
		fmt.Fprintf(b, "\treq.%s = %s\n", f.Name, raw)
		return
	}

	parse, convert := httpParseCall(basic)
	fmt.Fprintf(b, "\tif raw := %s; raw != \"\" {\n", raw)
	fmt.Fprintf(b, "\t\tv, err := %s\n", parse)
	b.WriteString("\t\tif err != nil {\n")
	fmt.Fprintf(b, "\t\t\t%s.WriteError(w, http.StatusBadRequest, %q)\n", e.http.Impl, fmt.Sprintf("%s is not a valid %s", httpSourceLabel(f), basic.Name()))
	b.WriteString("\t\t\treturn\n\t\t}\n")
	fmt.Fprintf(b, "\t\treq.%s = %s\n", f.Name, convert)
	b.WriteString("\t}\n")
}

func httpSourceLabel(f route.Field) string {
	switch f.Kind {
	case route.BindPath:
		return fmt.Sprintf("path segment %q", f.Param)
	case route.BindQuery:
		return fmt.Sprintf("query parameter %q", f.Param)
	case route.BindHeader:
		return fmt.Sprintf("header %q", f.Param)
	default:
		return fmt.Sprintf("form value %q", f.Param)
	}
}

// httpParseCall returns the strconv call for one scalar kind and the
// expression converting its result to the field's type. The scanner
// already restricted bound fields to these kinds.
func httpParseCall(basic *types.Basic) (parse, convert string) {
	switch basic.Kind() {
	case types.Bool:
		return "strconv.ParseBool(raw)", "v"
	case types.Int:
		return "strconv.ParseInt(raw, 10, 0)", "int(v)"
	case types.Int8:
		return "strconv.ParseInt(raw, 10, 8)", "int8(v)"
	case types.Int16:
		return "strconv.ParseInt(raw, 10, 16)", "int16(v)"
	case types.Int32:
		return "strconv.ParseInt(raw, 10, 32)", "int32(v)"
	case types.Int64:
		return "strconv.ParseInt(raw, 10, 64)", "v"
	case types.Uint:
		return "strconv.ParseUint(raw, 10, 0)", "uint(v)"
	case types.Uint8:
		return "strconv.ParseUint(raw, 10, 8)", "uint8(v)"
	case types.Uint16:
		return "strconv.ParseUint(raw, 10, 16)", "uint16(v)"
	case types.Uint32:
		return "strconv.ParseUint(raw, 10, 32)", "uint32(v)"
	case types.Uint64:
		return "strconv.ParseUint(raw, 10, 64)", "v"
	case types.Float32:
		return "strconv.ParseFloat(raw, 32)", "float32(v)"
	case types.Float64:
		return "strconv.ParseFloat(raw, 64)", "v"
	default:
		panic("emit: unreachable scalar kind " + basic.Name())
	}
}

// httpDepField is appArg's server-side twin: the App field an adapter
// reads one handler argument from.
func (e *emitter) httpDepField(dep *resolve.Node) string {
	if dep.Kind == resolve.NodeScopeAccessor {
		return e.rootByAccessor[dep.ScopeRoot].AccessorField
	}
	return e.varName[dep.Key]
}

// httpFuncRef renders the handler reference: bare within the injector's own
// package (self-import is illegal), qualified otherwise — same rule as
// provider calls.
func (e *emitter) httpFuncRef(fn *types.Func) string {
	if ident := e.pkgIdent(fn.Pkg()); ident != "" {
		return ident + "." + fn.Name()
	}
	return fn.Name()
}
