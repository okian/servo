package emit

import (
	"fmt"
	"go/types"
	"slices"
	"strings"

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

	RespondFunc  string // httpRespond
	WriteJSON    string // httpWriteJSON
	WriteError   string // httpWriteError
	WriteFailure string // httpWriteFailure
	MaxBodyConst string // defaultMaxBodyBytes, allocated only when needed
	needsBody    bool
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
	"app", "lc", "srv", "ready", "maxBody",
	"run",
	"s", "w", "r",
}

// httpReservedIdents are the parameters and locals emitted HTTP code names
// outright — same rationale as scopeReservedIdents: a user package taking
// one of these identifiers would shadow it exactly where generated code
// qualifies a type with it.
var httpReservedIdents = []string{
	"w", "r", "req", "res", "raw", "v", "q", "hs",
	"mux", "ln", "cfg", "lc", "ok", "h", "handler",
	"serveErr", "code", "msg", "payload", "s",
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
	e.imports.Add("errors", "errors")
	e.imports.Add("fmt", "fmt")
	e.imports.Add("encoding/json", "json")
	e.imports.Add("log/slog", "slog")
	e.imports.Add("net", "net")
	e.imports.Add("net/http", "http")
	e.imports.Add("strconv", "strconv")
	e.imports.Add("sync", "sync")
	e.imports.Add("sync/atomic", "atomic")
	e.imports.Reserve(httpReservedIdents...)

	h := &httpEmit{
		Plan:      plan,
		groupUses: map[string][]*resolve.HTTPUse{},
		routeUses: map[*resolve.HTTPRoute][]*resolve.HTTPUse{},
	}
	h.RespondFunc = e.types.AllocateName(e.testPrefixed("httpRespond"))
	h.WriteJSON = e.types.AllocateName(e.testPrefixed("httpWriteJSON"))
	h.WriteError = e.types.AllocateName(e.testPrefixed("httpWriteError"))
	h.WriteFailure = e.types.AllocateName(e.testPrefixed("httpWriteFailure"))

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
				h.needsBody = true
			}
		}
		h.Groups = append(h.Groups, g)
	}
	if h.needsBody {
		h.MaxBodyConst = e.types.AllocateName(e.testPrefixed("defaultMaxBodyBytes"))
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
// bookkeeping, the same triple every stoppable node gets.
func (e *emitter) httpAppFields() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	for _, g := range e.http.Groups {
		fmt.Fprintf(&b, "\t%s *%s\n", g.Field, g.ServerType)
		fmt.Fprintf(&b, "\t%sStopOnce sync.Once\n", g.Field)
		fmt.Fprintf(&b, "\t%sStopResult %s.NodeResult\n", g.Field, e.servoAlias)
	}
	return b.String()
}

// httpSetup is the tail of New: the servers are pure wiring — listeners
// bind in run() — so they can safely follow Init. A named group missing
// its HTTPConfig.Groups entry fails here, through the same
// Shutdown-and-join path an Init failure takes.
func (e *emitter) httpSetup() string {
	if e.http == nil {
		return ""
	}
	e.imports.Add("errors", "errors")
	var b strings.Builder
	for _, g := range e.http.Groups {
		fmt.Fprintf(&b, "\t%s, err := %s(a)\n", g.Field, g.NewFunc)
		b.WriteString("\tif err != nil {\n")
		b.WriteString("\t\treport := a.Shutdown(ctx)\n")
		b.WriteString("\t\treturn nil, errors.Join(err, report)\n")
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

// httpDecls renders every group's server type, constructor, adapters and
// run method, then the shared package-level helpers.
func (e *emitter) httpDecls() string {
	if e.http == nil {
		return ""
	}
	h := e.http
	var b strings.Builder

	if h.needsBody {
		fmt.Fprintf(&b, "const %s = 1 << 20\n\n", h.MaxBodyConst)
	}

	for _, g := range h.Groups {
		e.writeHTTPGroup(&b, g)
	}

	fmt.Fprintf(&b, "// %s writes one success response; a nil Json means status only, no body.\n", h.RespondFunc)
	fmt.Fprintf(&b, "func %s[T any](w http.ResponseWriter, code int, res %s.Json[T]) {\n", h.RespondFunc, e.servoAlias)
	b.WriteString("\tif res == nil {\n\t\tw.WriteHeader(code)\n\t\treturn\n\t}\n")
	fmt.Fprintf(&b, "\t%s(w, code, res.Value())\n}\n\n", h.WriteJSON)

	fmt.Fprintf(&b, "// %s maps a handler or extractor error onto the response: a status in\n", h.WriteFailure)
	b.WriteString("// the chain picks the code, 4xx bodies carry the message, 5xx bodies only\n")
	b.WriteString("// the canonical text — the wrapped detail is for the log, not the client.\n")
	fmt.Fprintf(&b, "func %s(w http.ResponseWriter, handler, route string, err error) {\n", h.WriteFailure)
	fmt.Fprintf(&b, "\tvar hs %s.HTTPStatus\n", e.servoAlias)
	b.WriteString("\tif !errors.As(err, &hs) {\n")
	b.WriteString("\t\tslog.Error(\"servo: handler \"+handler+\" failed\", \"route\", route, \"error\", err)\n")
	fmt.Fprintf(&b, "\t\t%s(w, http.StatusInternalServerError, \"Internal Server Error\")\n\t\treturn\n\t}\n", h.WriteError)
	b.WriteString("\tcode := hs.Code()\n")
	b.WriteString("\tif code >= 500 {\n")
	b.WriteString("\t\tslog.Error(\"servo: handler \"+handler+\" failed\", \"route\", route, \"status\", code, \"error\", err)\n")
	fmt.Fprintf(&b, "\t\t%s(w, code, hs.Error())\n\t\treturn\n\t}\n", h.WriteError)
	fmt.Fprintf(&b, "\t%s(w, code, err.Error())\n}\n\n", h.WriteError)

	fmt.Fprintf(&b, "func %s(w http.ResponseWriter, code int, payload any) {\n", h.WriteJSON)
	b.WriteString("\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
	b.WriteString("\tw.WriteHeader(code)\n")
	b.WriteString("\tif err := json.NewEncoder(w).Encode(payload); err != nil {\n")
	b.WriteString("\t\tslog.Error(\"servo: encoding response failed\", \"error\", err)\n\t}\n}\n\n")

	fmt.Fprintf(&b, "func %s(w http.ResponseWriter, code int, msg string) {\n", h.WriteError)
	b.WriteString("\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
	b.WriteString("\tw.WriteHeader(code)\n")
	b.WriteString("\t_ = json.NewEncoder(w).Encode(struct {\n\t\tError string `json:\"error\"`\n\t}{Error: msg})\n}\n\n")

	return b.String()
}

// writeHTTPGroup renders one group's server: struct, constructor with the
// middleware chain, adapters, and run method.
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
	b.WriteString("\t// ready flips once the listener is bound, which is the precise moment\n")
	fmt.Fprintf(b, "\t// Ready starts reporting ok for the %q node.\n", g.stopName())
	b.WriteString("\tready atomic.Bool\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "func %s(a *%s) (*%s, error) {\n", g.NewFunc, e.appType(), g.ServerType)
	fmt.Fprintf(b, "\tcfg := a.%s\n", cfgField)
	if g.Name == "" {
		fmt.Fprintf(b, "\tlc := %s.HTTPListener{\n", e.servoAlias)
		b.WriteString("\t\tIP:           cfg.IP,\n")
		b.WriteString("\t\tPort:         cfg.Port,\n")
		b.WriteString("\t\tCertFile:     cfg.CertFile,\n")
		b.WriteString("\t\tKeyFile:      cfg.KeyFile,\n")
		b.WriteString("\t\tMaxBodyBytes: cfg.MaxBodyBytes,\n")
		b.WriteString("\t\tReadTimeout:  cfg.ReadTimeout,\n")
		b.WriteString("\t\tWriteTimeout: cfg.WriteTimeout,\n")
		b.WriteString("\t\tIdleTimeout:  cfg.IdleTimeout,\n")
		b.WriteString("\t}\n")
	} else {
		fmt.Fprintf(b, "\tlc, ok := cfg.Groups[%q]\n", g.Name)
		b.WriteString("\tif !ok {\n")
		fmt.Fprintf(b, "\t\treturn nil, fmt.Errorf(\"http: group %%q declared in the spec but missing from HTTPConfig.Groups\", %q)\n", g.Name)
		b.WriteString("\t}\n")
	}
	fmt.Fprintf(b, "\ts := &%s{app: a, lc: lc}\n", g.ServerType)
	if g.needsBody {
		fmt.Fprintf(b, "\ts.maxBody = lc.MaxBodyBytes\n\tif s.maxBody <= 0 {\n\t\ts.maxBody = %s\n\t}\n", h.MaxBodyConst)
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
			fmt.Fprintf(b, "\t\th = a.%s.Middleware(h)\n", e.varName[u.Node.Key])
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
			fmt.Fprintf(b, "\thandler = a.%s.Middleware(handler)\n", e.varName[u.Node.Key])
		}
		handlerExpr = "handler"
	}
	b.WriteString("\ts.srv = &http.Server{\n")
	b.WriteString("\t\tAddr:         net.JoinHostPort(lc.IP, strconv.FormatUint(uint64(lc.Port), 10)),\n")
	fmt.Fprintf(b, "\t\tHandler:      %s,\n", handlerExpr)
	b.WriteString("\t\tReadTimeout:  lc.ReadTimeout,\n")
	b.WriteString("\t\tWriteTimeout: lc.WriteTimeout,\n")
	b.WriteString("\t\tIdleTimeout:  lc.IdleTimeout,\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn s, nil\n}\n\n")

	for _, re := range g.Routes {
		e.writeHTTPAdapter(b, g, re)
	}

	e.writeHTTPRun(b, g)
}

// writeHTTPRun emits one group server's Run half: bind explicitly (so
// ready is a precise moment), serve on a goroutine, return when ctx ends
// or serving fails — the same shape a hand-written server component has.
func (e *emitter) writeHTTPRun(b *strings.Builder, g *httpGroupEmit) {
	fmt.Fprintf(b, "func (s *%s) run(ctx context.Context) error {\n", g.ServerType)
	b.WriteString("\tln, err := net.Listen(\"tcp\", s.srv.Addr)\n")
	fmt.Fprintf(b, "\tif err != nil {\n\t\treturn fmt.Errorf(\"%s: listen %%s: %%w\", s.srv.Addr, err)\n\t}\n", g.stopName())
	b.WriteString("\ts.ready.Store(true)\n")
	b.WriteString("\tserveErr := make(chan error, 1)\n")
	b.WriteString("\tgo func() {\n")
	b.WriteString("\t\tvar err error\n")
	b.WriteString("\t\tif s.lc.CertFile != \"\" && s.lc.KeyFile != \"\" {\n")
	b.WriteString("\t\t\terr = s.srv.ServeTLS(ln, s.lc.CertFile, s.lc.KeyFile)\n")
	b.WriteString("\t\t} else {\n\t\t\terr = s.srv.Serve(ln)\n\t\t}\n")
	b.WriteString("\t\tif err != nil && !errors.Is(err, http.ErrServerClosed) {\n\t\t\tserveErr <- err\n\t\t\treturn\n\t\t}\n")
	b.WriteString("\t\tserveErr <- nil\n\t}()\n")
	b.WriteString("\tselect {\n\tcase <-ctx.Done():\n\t\treturn nil\n\tcase err := <-serveErr:\n\t\treturn err\n\t}\n}\n\n")
}

// httpStopMethods are the App-level stops, sequenced first in Shutdown:
// the servers are the inbound edges, so they drain before anything they
// depend on. The nil guard covers construction-failure rollback, which
// calls Shutdown before the servers are wired.
func (e *emitter) httpStopMethod() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	for _, g := range e.http.Groups {
		name := g.stopName()
		fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.NodeResult {\n", e.appType(), g.StopMethod, e.servoAlias)
		fmt.Fprintf(&b, "\ta.%sStopOnce.Do(func() {\n", g.Field)
		fmt.Fprintf(&b, "\t\tif a.%s == nil {\n", g.Field)
		fmt.Fprintf(&b, "\t\t\ta.%sStopResult = %s.NodeResult{Name: %q, Status: %s.StatusOK}\n", g.Field, e.servoAlias, name, e.servoAlias)
		b.WriteString("\t\t\treturn\n\t\t}\n")
		fmt.Fprintf(&b, "\t\ta.%sStopResult = %s.RunStop(ctx, %s.DefaultStopBudget, %q, a.%s.srv.Shutdown)\n", g.Field, e.servoAlias, e.servoAlias, name, g.Field)
		b.WriteString("\t})\n")
		fmt.Fprintf(&b, "\treturn a.%sStopResult\n", g.Field)
		b.WriteString("}\n\n")
	}
	return b.String()
}

// writeHTTPAdapter emits one route's http.HandlerFunc: typed decode,
// per-request extraction, the handler call with graph-resolved arguments,
// and the status contract.
func (e *emitter) writeHTTPAdapter(b *strings.Builder, g *httpGroupEmit, re *httpRouteEmit) {
	h := e.http
	rt := re.R.Route
	routeLabel := rt.Method + " " + rt.Pattern

	fmt.Fprintf(b, "func (s *%s) %s(w http.ResponseWriter, r *http.Request) {\n", g.ServerType, re.Adapter)

	args := []string{"r.Context()"}
	if rt.Req != nil {
		fmt.Fprintf(b, "\treq := &%s{}\n", e.qualifiedTypeString(rt.Req.Named))
		if rt.Req.HasBody {
			b.WriteString("\t// The body decodes first, so the explicitly bound sources below\n")
			b.WriteString("\t// overwrite anything it could smuggle into a same-named field.\n")
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\tif err := json.NewDecoder(r.Body).Decode(req); err != nil {\n")
			fmt.Fprintf(b, "\t\t%s(w, http.StatusBadRequest, \"malformed request body\")\n\t\treturn\n\t}\n", h.WriteError)
		}
		if rt.Req.HasForm {
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\t// ParseMultipartForm falls through to ParseForm on urlencoded\n")
			b.WriteString("\t// bodies, reporting ErrNotMultipart after already parsing them.\n")
			b.WriteString("\tif err := r.ParseMultipartForm(s.maxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {\n")
			fmt.Fprintf(b, "\t\t%s(w, http.StatusBadRequest, \"malformed form body\")\n\t\treturn\n\t}\n", h.WriteError)
		}
		if httpNeedsQuery(rt) {
			b.WriteString("\tq := r.URL.Query()\n")
		}
		for _, f := range rt.Req.Fields {
			e.writeHTTPFieldDecode(b, f)
		}
		args = append(args, "req")
	}

	// Extracted parameters are produced per request, before the handler,
	// each failing through the shared status mapping.
	extractedNames := NewNameAllocator()
	for _, reserved := range []string{"w", "r", "req", "q", "raw", "v", "res", "err", "hs", "s"} {
		extractedNames.AllocateName(reserved)
	}
	for _, arg := range re.R.Args {
		if arg.Node != nil {
			args = append(args, "s.app."+e.httpDepField(arg.Node))
			continue
		}
		local := extractedNames.AllocateName(baseName(arg.Extractor.ProducesType))
		fmt.Fprintf(b, "\t%s, err := s.app.%s.Extract(r)\n", local, e.varName[arg.Extractor.Node.Key])
		b.WriteString("\tif err != nil {\n")
		fmt.Fprintf(b, "\t\t%s(w, %q, %q, err)\n\t\treturn\n\t}\n", h.WriteFailure, rt.Name, routeLabel)
		args = append(args, local)
	}

	fmt.Fprintf(b, "\tres, err := %s(%s)\n", e.httpFuncRef(rt.Func), strings.Join(args, ", "))
	b.WriteString("\tif err != nil {\n")
	fmt.Fprintf(b, "\t\tvar hs %s.HTTPStatus\n", e.servoAlias)
	b.WriteString("\t\tif errors.As(err, &hs) && hs.Code() < 300 {\n")
	b.WriteString("\t\t\t// A 2xx status in the error position is the contract's way of\n")
	b.WriteString("\t\t\t// picking a success code other than 200.\n")
	fmt.Fprintf(b, "\t\t\t%s(w, hs.Code(), res)\n\t\t\treturn\n\t\t}\n", h.RespondFunc)
	fmt.Fprintf(b, "\t\t%s(w, %q, %q, err)\n\t\treturn\n\t}\n", h.WriteFailure, rt.Name, routeLabel)
	fmt.Fprintf(b, "\t%s(w, http.StatusOK, res)\n", h.RespondFunc)
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
	fmt.Fprintf(b, "\t\t\t%s(w, http.StatusBadRequest, %q)\n", e.http.WriteError, fmt.Sprintf("%s is not a valid %s", httpSourceLabel(f), basic.Name()))
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
