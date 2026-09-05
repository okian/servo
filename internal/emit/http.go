package emit

import (
	"fmt"
	"go/types"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/internal/route"
)

// httpEmit is the resolved HTTP plan plus every identifier the generated
// server needs, allocated once up front like scopeEmit's — the server is
// the same kind of synthetic machinery a scope is: emitted, not a node.
type httpEmit struct {
	Plan *resolve.HTTPPlan

	ServerType   string // httpServer
	NewFunc      string // newHTTPServer
	RespondFunc  string // httpRespond
	MaxBodyConst string // defaultMaxBodyBytes, allocated only when needed
	Field        string // App field holding the server
	StopMethod   string // stopHttpServer
	Routes       []*httpRouteEmit

	// needsBody is whether any route reads a request body (JSON or form),
	// which is what decides whether the maxBody field and its default
	// const exist — emitted conditionally so a body-less API doesn't carry
	// an unused field for the linter to flag.
	needsBody bool
}

type httpRouteEmit struct {
	R       *resolve.HTTPRoute
	Adapter string // method name on the server type
}

// httpServerReservedMembers is every field and method the server type
// declares itself; adapters share the namespace (a Go field and method
// collide), so they are claimed before any adapter gets a name.
var httpServerReservedMembers = []string{
	"app", "cfg", "srv", "ready", "maxBody",
	"run", "writeJSON", "writeError",
	"s", "w", "r",
}

// httpReservedIdents are the parameters and locals emitted HTTP code names
// outright — same rationale as scopeReservedIdents: a user package taking
// one of these identifiers would shadow it exactly where generated code
// qualifies a type with it.
var httpReservedIdents = []string{
	"w", "r", "req", "res", "raw", "v", "q", "hs",
	"mux", "ln", "cfg", "serveErr", "code", "msg", "payload", "s",
}

// planHTTP allocates every HTTP identifier and registers the imports the
// emitted server hard-codes. Runs after planScopes so scope declarations
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

	h := &httpEmit{Plan: plan}
	h.ServerType = e.types.AllocateName(e.testPrefixed("httpServer"))
	h.NewFunc = e.types.AllocateName("new" + capitalize(e.testPrefixed("httpServer")))
	h.RespondFunc = e.types.AllocateName(e.testPrefixed("httpRespond"))
	h.Field = allocateAppField(e.names, "httpServer")
	h.StopMethod = "stop" + capitalize(h.Field)

	adapterNames := NewNameAllocator()
	for _, reserved := range httpServerReservedMembers {
		adapterNames.AllocateName(reserved)
	}
	// Handlers sharing a bare name get package-qualified adapters, decided
	// up front the way baseNamesFor decides field names: whoever came first
	// must not keep the ambiguous name.
	count := map[string]int{}
	for _, hr := range plan.Routes {
		count[hr.Route.Func.Name()]++
	}
	for _, hr := range plan.Routes {
		base := hr.Route.Func.Name()
		if count[base] > 1 && hr.Route.Func.Pkg() != nil {
			base = capitalize(hr.Route.Func.Pkg().Name()) + capitalize(base)
		}
		re := &httpRouteEmit{R: hr, Adapter: adapterNames.AllocateName("handle" + capitalize(base))}
		h.Routes = append(h.Routes, re)
		if req := hr.Route.Req; req != nil && (req.HasBody || req.HasForm) {
			h.needsBody = true
		}
	}
	if h.needsBody {
		h.MaxBodyConst = e.types.AllocateName(e.testPrefixed("defaultMaxBodyBytes"))
	}
	e.http = h
}

// httpAppFields renders the App's server field and its stop bookkeeping,
// the same triple every stoppable node gets.
func (e *emitter) httpAppFields() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\t%s *%s\n", e.http.Field, e.http.ServerType)
	fmt.Fprintf(&b, "\t%sStopOnce sync.Once\n", e.http.Field)
	fmt.Fprintf(&b, "\t%sStopResult %s.NodeResult\n", e.http.Field, e.servoAlias)
	return b.String()
}

// httpSetup is the last line of New: the server is pure wiring — the
// listener binds in run() — so it can safely follow Init, and construct
// last means its dependencies and config are all in place.
func (e *emitter) httpSetup() string {
	if e.http == nil {
		return ""
	}
	return fmt.Sprintf("\ta.%s = %s(a)\n\n", e.http.Field, e.http.NewFunc)
}

// httpHeader documents the routing table in the generated file's header,
// beside the resolved graph and the scopes.
func (e *emitter) httpHeader() string {
	if e.http == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "//\n// http (servo.HTTP() at %s):\n", e.posString(e.http.Plan.Pos))
	fmt.Fprintf(&b, "//   config: %s\n", e.http.Plan.Config.Key.String())
	for _, re := range e.http.Routes {
		rt := re.R.Route
		deps := "none"
		if len(rt.Deps) > 0 {
			names := make([]string, len(rt.Deps))
			for i, d := range rt.Deps {
				names[i] = d.String()
			}
			deps = strings.Join(names, ", ")
		}
		fmt.Fprintf(&b, "//   %s %s -> %s (%s)\n//         deps: %s\n", rt.Method, rt.Pattern, rt.Name, e.posString(rt.Pos), deps)
	}
	return b.String()
}

// httpDecls renders the server type, its constructor, one adapter per
// route, the typed respond helper, and the write/run plumbing.
func (e *emitter) httpDecls() string {
	if e.http == nil {
		return ""
	}
	h := e.http
	var b strings.Builder
	cfgField := e.varName[h.Plan.Config.Key]

	fmt.Fprintf(&b, "// %s serves the //servo: routes declared across this module. It is\n", h.ServerType)
	fmt.Fprintf(&b, "// generated machinery, not a graph node: its dependencies are ordinary\n")
	fmt.Fprintf(&b, "// singletons on the %s, resolved once at construction.\n", e.appType())
	fmt.Fprintf(&b, "type %s struct {\n", h.ServerType)
	fmt.Fprintf(&b, "\tapp *%s\n", e.appType())
	fmt.Fprintf(&b, "\tcfg *%s.HTTPConfig\n", e.servoAlias)
	b.WriteString("\tsrv *http.Server\n")
	if h.needsBody {
		b.WriteString("\tmaxBody int64\n")
	}
	b.WriteString("\t// ready flips once the listener is bound, which is the precise moment\n")
	b.WriteString("\t// Ready starts reporting ok for the \"http\" node.\n")
	b.WriteString("\tready atomic.Bool\n")
	b.WriteString("}\n\n")

	if h.needsBody {
		fmt.Fprintf(&b, "const %s = 1 << 20\n\n", h.MaxBodyConst)
	}

	fmt.Fprintf(&b, "func %s(a *%s) *%s {\n", h.NewFunc, e.appType(), h.ServerType)
	fmt.Fprintf(&b, "\tcfg := a.%s\n", cfgField)
	fmt.Fprintf(&b, "\ts := &%s{app: a, cfg: cfg}\n", h.ServerType)
	if h.needsBody {
		fmt.Fprintf(&b, "\ts.maxBody = cfg.MaxBodyBytes\n\tif s.maxBody <= 0 {\n\t\ts.maxBody = %s\n\t}\n", h.MaxBodyConst)
	}
	b.WriteString("\tmux := http.NewServeMux()\n")
	for _, re := range h.Routes {
		fmt.Fprintf(&b, "\tmux.HandleFunc(%q, s.%s)\n", re.R.Route.Method+" "+re.R.Route.Pattern, re.Adapter)
	}
	b.WriteString("\ts.srv = &http.Server{\n")
	b.WriteString("\t\tAddr:         net.JoinHostPort(cfg.IP, strconv.FormatUint(uint64(cfg.Port), 10)),\n")
	b.WriteString("\t\tHandler:      mux,\n")
	b.WriteString("\t\tReadTimeout:  cfg.ReadTimeout,\n")
	b.WriteString("\t\tWriteTimeout: cfg.WriteTimeout,\n")
	b.WriteString("\t\tIdleTimeout:  cfg.IdleTimeout,\n")
	b.WriteString("\t}\n")
	b.WriteString("\treturn s\n}\n\n")

	for _, re := range h.Routes {
		e.writeHTTPAdapter(&b, re)
	}

	fmt.Fprintf(&b, "// %s writes one success response; a nil Json means status only, no body.\n", h.RespondFunc)
	fmt.Fprintf(&b, "func %s[T any](s *%s, w http.ResponseWriter, code int, res %s.Json[T]) {\n", h.RespondFunc, h.ServerType, e.servoAlias)
	b.WriteString("\tif res == nil {\n\t\tw.WriteHeader(code)\n\t\treturn\n\t}\n")
	b.WriteString("\ts.writeJSON(w, code, res.Value())\n}\n\n")

	fmt.Fprintf(&b, "func (s *%s) writeJSON(w http.ResponseWriter, code int, payload any) {\n", h.ServerType)
	b.WriteString("\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
	b.WriteString("\tw.WriteHeader(code)\n")
	b.WriteString("\tif err := json.NewEncoder(w).Encode(payload); err != nil {\n")
	b.WriteString("\t\tslog.Error(\"servo: encoding response failed\", \"error\", err)\n\t}\n}\n\n")

	fmt.Fprintf(&b, "func (s *%s) writeError(w http.ResponseWriter, code int, msg string) {\n", h.ServerType)
	b.WriteString("\tw.Header().Set(\"Content-Type\", \"application/json\")\n")
	b.WriteString("\tw.WriteHeader(code)\n")
	b.WriteString("\t_ = json.NewEncoder(w).Encode(struct {\n\t\tError string `json:\"error\"`\n\t}{Error: msg})\n}\n\n")

	e.writeHTTPRun(&b)
	return b.String()
}

// writeHTTPRun emits the server's Run half: bind explicitly (so ready is a
// precise moment), serve on a goroutine, return when ctx ends or serving
// fails — the same shape a hand-written server component has.
func (e *emitter) writeHTTPRun(b *strings.Builder) {
	h := e.http
	fmt.Fprintf(b, "func (s *%s) run(ctx context.Context) error {\n", h.ServerType)
	b.WriteString("\tln, err := net.Listen(\"tcp\", s.srv.Addr)\n")
	b.WriteString("\tif err != nil {\n\t\treturn fmt.Errorf(\"http: listen %s: %w\", s.srv.Addr, err)\n\t}\n")
	b.WriteString("\ts.ready.Store(true)\n")
	b.WriteString("\tserveErr := make(chan error, 1)\n")
	b.WriteString("\tgo func() {\n")
	b.WriteString("\t\tvar err error\n")
	b.WriteString("\t\tif s.cfg.CertFile != \"\" && s.cfg.KeyFile != \"\" {\n")
	b.WriteString("\t\t\terr = s.srv.ServeTLS(ln, s.cfg.CertFile, s.cfg.KeyFile)\n")
	b.WriteString("\t\t} else {\n\t\t\terr = s.srv.Serve(ln)\n\t\t}\n")
	b.WriteString("\t\tif err != nil && !errors.Is(err, http.ErrServerClosed) {\n\t\t\tserveErr <- err\n\t\t\treturn\n\t\t}\n")
	b.WriteString("\t\tserveErr <- nil\n\t}()\n")
	b.WriteString("\tselect {\n\tcase <-ctx.Done():\n\t\treturn nil\n\tcase err := <-serveErr:\n\t\treturn err\n\t}\n}\n\n")
}

// httpStopMethod is the App-level stop, sequenced first in Shutdown: the
// server is the inbound edge, so it drains before anything it depends on.
// The nil guard covers Init-failure rollback, which calls Shutdown before
// the server is wired.
func (e *emitter) httpStopMethod() string {
	if e.http == nil {
		return ""
	}
	h := e.http
	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.NodeResult {\n", e.appType(), h.StopMethod, e.servoAlias)
	fmt.Fprintf(&b, "\ta.%sStopOnce.Do(func() {\n", h.Field)
	fmt.Fprintf(&b, "\t\tif a.%s == nil {\n", h.Field)
	fmt.Fprintf(&b, "\t\t\ta.%sStopResult = %s.NodeResult{Name: \"http\", Status: %s.StatusOK}\n", h.Field, e.servoAlias, e.servoAlias)
	b.WriteString("\t\t\treturn\n\t\t}\n")
	fmt.Fprintf(&b, "\t\ta.%sStopResult = %s.RunStop(ctx, %s.DefaultStopBudget, \"http\", a.%s.srv.Shutdown)\n", h.Field, e.servoAlias, e.servoAlias, h.Field)
	b.WriteString("\t})\n")
	fmt.Fprintf(&b, "\treturn a.%sStopResult\n", h.Field)
	b.WriteString("}\n\n")
	return b.String()
}

// writeHTTPAdapter emits one route's http.HandlerFunc: typed decode, the
// handler call with graph-resolved arguments, and the status contract.
func (e *emitter) writeHTTPAdapter(b *strings.Builder, re *httpRouteEmit) {
	h := e.http
	rt := re.R.Route
	routeLabel := rt.Method + " " + rt.Pattern
	logCall := fmt.Sprintf("slog.Error(\"servo: handler %s failed\", \"route\", %q, \"error\", err)", rt.Name, routeLabel)

	fmt.Fprintf(b, "func (s *%s) %s(w http.ResponseWriter, r *http.Request) {\n", h.ServerType, re.Adapter)

	args := []string{"r.Context()"}
	if rt.Req != nil {
		fmt.Fprintf(b, "\treq := &%s{}\n", e.qualifiedTypeString(rt.Req.Named))
		if rt.Req.HasBody {
			b.WriteString("\t// The body decodes first, so the explicitly bound sources below\n")
			b.WriteString("\t// overwrite anything it could smuggle into a same-named field.\n")
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\tif err := json.NewDecoder(r.Body).Decode(req); err != nil {\n")
			b.WriteString("\t\ts.writeError(w, http.StatusBadRequest, \"malformed request body\")\n\t\treturn\n\t}\n")
		}
		if rt.Req.HasForm {
			b.WriteString("\tr.Body = http.MaxBytesReader(w, r.Body, s.maxBody)\n")
			b.WriteString("\t// ParseMultipartForm falls through to ParseForm on urlencoded\n")
			b.WriteString("\t// bodies, reporting ErrNotMultipart after already parsing them.\n")
			b.WriteString("\tif err := r.ParseMultipartForm(s.maxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {\n")
			b.WriteString("\t\ts.writeError(w, http.StatusBadRequest, \"malformed form body\")\n\t\treturn\n\t}\n")
		}
		if httpNeedsQuery(rt) {
			b.WriteString("\tq := r.URL.Query()\n")
		}
		for _, f := range rt.Req.Fields {
			e.writeHTTPFieldDecode(b, f)
		}
		args = append(args, "req")
	}
	for _, arg := range re.R.Args {
		if arg.Node == nil {
			continue // extracted parameters are emitted with Task 6's extractor support
		}
		args = append(args, "s.app."+e.httpDepField(arg.Node))
	}

	fmt.Fprintf(b, "\tres, err := %s(%s)\n", e.httpFuncRef(rt.Func), strings.Join(args, ", "))
	b.WriteString("\tif err != nil {\n")
	fmt.Fprintf(b, "\t\tvar hs %s.HTTPStatus\n", e.servoAlias)
	b.WriteString("\t\tif !errors.As(err, &hs) {\n")
	fmt.Fprintf(b, "\t\t\t%s\n", logCall)
	b.WriteString("\t\t\ts.writeError(w, http.StatusInternalServerError, \"Internal Server Error\")\n\t\t\treturn\n\t\t}\n")
	b.WriteString("\t\tcode := hs.Code()\n")
	b.WriteString("\t\tswitch {\n")
	b.WriteString("\t\tcase code < 300:\n")
	b.WriteString("\t\t\t// A 2xx status in the error position is the contract's way of\n")
	b.WriteString("\t\t\t// picking a success code other than 200.\n")
	fmt.Fprintf(b, "\t\t\t%s(s, w, code, res)\n", h.RespondFunc)
	b.WriteString("\t\tcase code < 500:\n")
	b.WriteString("\t\t\ts.writeError(w, code, err.Error())\n")
	b.WriteString("\t\tdefault:\n")
	b.WriteString("\t\t\t// 5xx bodies carry only the canonical text; the wrapped detail\n")
	b.WriteString("\t\t\t// is for the log, not the client.\n")
	fmt.Fprintf(b, "\t\t\t%s\n", logCall)
	b.WriteString("\t\t\ts.writeError(w, code, hs.Error())\n")
	b.WriteString("\t\t}\n\t\treturn\n\t}\n")
	fmt.Fprintf(b, "\t%s(s, w, http.StatusOK, res)\n", h.RespondFunc)
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
	fmt.Fprintf(b, "\t\t\ts.writeError(w, http.StatusBadRequest, %q)\n", fmt.Sprintf("%s is not a valid %s", httpSourceLabel(f), basic.Name()))
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
// package, qualified otherwise — same rule as provider calls.
func (e *emitter) httpFuncRef(fn *types.Func) string {
	if ident := e.pkgIdent(fn.Pkg()); ident != "" {
		return ident + "." + fn.Name()
	}
	return fn.Name()
}
