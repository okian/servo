package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/internal/route"
)

// httpAppSrc keeps the handlers OUTSIDE the injector package, so the
// emitted adapters must reference them (and the request structs) with a
// package qualifier. It carries one route per feature: typed binding with
// a dependency, a JSON body, a bare handler, a telemetry-group route, an
// extracted parameter, a body beside an extracted parameter (which pins
// that extraction runs first), and middleware at all three levels — one of
// them a plain func(http.Handler) http.Handler. Repo has an Init so the
// servers' place relative to the Init phase is visible.
const httpAppSrc = `
package httpapp

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type Repo struct{}
func NewRepo() *Repo { return &Repo{} }
func (r *Repo) Init(ctx context.Context) error { return nil }
func (r *Repo) Stop(ctx context.Context) error { return nil }

func NewHTTPConfig() *servo.HTTPConfig { return &servo.HTTPConfig{} }

type OrderReq struct {
	Category string ` + "`path:\"category\"`" + `
	Page     int    ` + "`query:\"page\"`" + `
}
type OrderResp struct{ ID string }

//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, r *Repo) (servo.Json[*OrderResp], error) {
	return servo.JSON(&OrderResp{}), nil
}

type NoteReq struct {
	Text string ` + "`json:\"text\"`" + `
}

//servo:post /notes
func CreateNote(ctx context.Context, req *NoteReq) (servo.Json[*OrderResp], error) {
	return nil, servo.Status.CREATED
}

//servo:get /health
func Health(ctx context.Context) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*OrderResp], error) { return nil, nil }

type User struct{ Name string }

type UserExtractor struct{}
func NewUserExtractor() *UserExtractor { return &UserExtractor{} }
func (e *UserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

//servo:get /me
func Me(ctx context.Context, u *User) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:post /me/notes
func UpdateMe(ctx context.Context, req *NoteReq, u *User) (servo.Json[*OrderResp], error) { return nil, nil }

type Guard struct{}
func NewGuard() *Guard { return &Guard{} }
func (m *Guard) Middleware(next http.Handler) http.Handler { return next }

type Auth struct{}
func NewAuth() *Auth { return &Auth{} }
func (m *Auth) Middleware(next http.Handler) http.Handler { return next }

type Audit struct{}
func NewAudit() *Audit { return &Audit{} }
func (m *Audit) Middleware(next http.Handler) http.Handler { return next }

// Tag is middleware in the shape otelhttp, chi and gorilla/handlers ship:
// a plain function, attached as itself.
type Tag func(http.Handler) http.Handler
func NewTag() Tag { return func(next http.Handler) http.Handler { return next } }

// Explicit is a func type that declares a Middleware method anyway; the
// method is the explicit form, so it wins.
type Explicit func(http.Handler) http.Handler
func (m Explicit) Middleware(next http.Handler) http.Handler { return m(next) }

// Httpapp is an extracted type named after the package its handler lives
// in: the adapter's local for it must not hide the package the very next
// line calls the handler through.
type Httpapp struct{}

type HttpappExtractor struct{}
func NewHttpappExtractor() *HttpappExtractor { return &HttpappExtractor{} }
func (e *HttpappExtractor) Extract(r *http.Request) (*Httpapp, error) { return &Httpapp{}, nil }

//servo:get /shadow
func Shadow(ctx context.Context, h *Httpapp) (servo.Json[*OrderResp], error) { return nil, nil }
`

const httpMainSrc = `
package main
`

var (
	stdPkgMu    sync.Mutex
	stdPkgCache = map[string]*packages.Package{}
)

// loadStdPkg memoizes by path: net/http is the heaviest thing these tests
// load, and loading it once instead of per-test keeps the package fast.
func loadStdPkg(t *testing.T, path string) *packages.Package {
	t.Helper()
	stdPkgMu.Lock()
	defer stdPkgMu.Unlock()
	if p, ok := stdPkgCache[path]; ok {
		return p
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, path)
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("load %s: %v", path, err)
	}
	stdPkgCache[path] = pkgs[0]
	return pkgs[0]
}

func buildHTTPResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	return buildHTTPResolvedWith(t, nil)
}

// buildHTTPResolvedWith is buildHTTPResolved with a chance to change the
// resolver's input first — fewer groups, other routes, more declarations —
// given the fixture's own package to look types up in.
func buildHTTPResolvedWith(t *testing.T, edit func(in *resolve.Input, app *types.Package)) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	httpPkg := loadStdPkg(t, "net/http")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	fset := token.NewFileSet()
	importer := newPkgImporter(servoPkg, httpPkg)
	mod := &packages.Module{Path: "example.com", Main: true}

	parse := func(path, src string) *packages.Package {
		f, err := parser.ParseFile(fset, path+"/fixture.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Instances: map[*ast.Ident]types.Instance{}}
		conf := types.Config{Importer: importer}
		pkg, err := conf.Check(path, fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("typecheck %s: %v", path, err)
		}
		importer.byPath[path] = pkg
		return &packages.Package{
			Name: pkg.Name(), PkgPath: path,
			Types: pkg, TypesInfo: info, Fset: fset, Syntax: []*ast.File{f}, Module: mod,
		}
	}

	appPkg := parse("example.com/httpapp", httpAppSrc)
	mainPkg := parse("example.com/httpapp/cmd/app", httpMainSrc)
	pkgs := []*packages.Package{appPkg, mainPkg}

	candidates, _ := graph.ScanCandidates(pkgs, mainPkg.PkgPath)
	routes, rdiags := route.Scan(pkgs, servoPkg.Types)
	if len(rdiags) != 0 {
		t.Fatalf("fixture scan diagnostics: %v", rdiags)
	}

	ptr := func(name string) types.Type {
		return types.NewPointer(appPkg.Types.Scope().Lookup(name).Type())
	}
	key := func(name string) graph.Key { return graph.NewKey(ptr(name), "") }

	spec := &load.Spec{InjectorPkg: mainPkg}
	ck, ct := route.HTTPConfigKey(servoPkg.Types)
	tag := appPkg.Types.Scope().Lookup("Tag").Type()
	in := resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{appPkg.PkgPath: true, mainPkg.PkgPath: true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP: &resolve.HTTPInput{
			Pos:    token.Position{Filename: "spec.go", Line: 11},
			Routes: routes,
			Groups: []load.GroupDecl{{Name: "telemetry", Pos: token.Position{Filename: "spec.go", Line: 12}}},
			Uses: []load.UseDecl{
				{Type: key("Guard"), TypeT: ptr("Guard"), Pos: token.Position{Filename: "spec.go", Line: 13}},
				{Type: key("Auth"), TypeT: ptr("Auth"), Groups: []string{"telemetry"}, Pos: token.Position{Filename: "spec.go", Line: 14}},
				{Type: key("Audit"), TypeT: ptr("Audit"), Routes: []load.RouteSel{{Pattern: "POST /order/{category}/", Pos: token.Position{Filename: "spec.go", Line: 15}}}, Pos: token.Position{Filename: "spec.go", Line: 15}},
				{Type: graph.NewKey(tag, ""), TypeT: tag, Pos: token.Position{Filename: "spec.go", Line: 18}},
			},
			Extracts: []load.ExtractDecl{
				{Type: key("UserExtractor"), TypeT: ptr("UserExtractor"), Pos: token.Position{Filename: "spec.go", Line: 16}},
				{Type: key("HttpappExtractor"), TypeT: ptr("HttpappExtractor"), Pos: token.Position{Filename: "spec.go", Line: 17}},
			},
			ConfigKey:  ck,
			ConfigType: ct,
		},
	}
	if edit != nil {
		edit(&in, appPkg.Types)
	}
	resolved, diags := resolve.Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

func TestEmitHTTPServer(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	wantSubstrings := []string{
		// One server type per group, each constructed fallibly after every
		// node and before the Init phase.
		"type httpServer struct",
		"type httpTelemetryServer struct",
		"func newHttpServer(a *App) (*httpServer, error)",
		"func newHttpTelemetryServer(a *App) (*httpTelemetryServer, error)",
		"a.httpServer = httpServer",
		"a.httpTelemetryServer = httpTelemetryServer",
		// The runtime picks each group's listener — the flat config fields
		// for the default group, a Groups entry (or an error naming it) for
		// the rest — and builds the server from it, key pair and all.
		`lc, err := servoimpl.Listener(a.hTTPConfig, "")`,
		`lc, err := servoimpl.Listener(a.hTTPConfig, "telemetry")`,
		`s.srv, s.cancel, err = servoimpl.NewServer("http", lc, handler)`,
		`s.srv, s.cancel, err = servoimpl.NewServer("http:telemetry", lc, handler)`,
		"s.maxBody = servoimpl.BodyLimit(lc)",
		"cancel context.CancelCauseFunc",
		// Routes land on their own group's mux.
		`mux.HandleFunc("GET /health", s.handleHealth)`,
		`mux.HandleFunc("GET /healthz", s.handleHealthz)`,
		`mux.HandleFunc("POST /notes", s.handleCreateNote)`,
		// Middleware: route-level wraps the one handler, group- and
		// server-level wrap the mux.
		"h = a.audit.Middleware(h)",
		`mux.Handle("POST /order/{category}/", h)`,
		"handler = a.auth.Middleware(handler)",
		"handler = a.guard.Middleware(handler)",
		// Extracted parameter: produced per request, failure through the
		// shared status mapping, then handed to the handler.
		"user, err := s.app.userExtractor.Extract(r)",
		`servoimpl.WriteFailure(w, r, "httpapp.Me", "GET /me", err)`,
		"httpapp.Me(r.Context(), user)",
		// The status contract is the runtime's, one call per adapter, with
		// the request for its context and the handler and route for its log.
		`servoimpl.Respond(w, r, "httpapp.Me", "GET /me", res, err)`,
		`servoimpl.Respond(w, r, "httpapp.Order", "POST /order/{category}/", res, err)`,
		// An extracted local never takes the name of a package the body
		// still has to call into — here the handlers' own package.
		"httpapp2, err := s.app.httpappExtractor.Extract(r)",
		"httpapp.Shadow(r.Context(), httpapp2)",
		// A body over the limit is a 413, decoded through one helper.
		`servoimpl.BodyError(w, err, "malformed request body")`,
		// The query is parsed for its error, which r.URL.Query() drops.
		"q, err := url.ParseQuery(r.URL.RawQuery)",
		`servoimpl.WriteError(w, http.StatusBadRequest, "malformed query string")`,
		`servoimpl.WriteError(w, http.StatusBadRequest, "query parameter \"page\" is not a valid int")`,
		// Typed decode and the handler call are unchanged in spirit.
		`req.Category = r.PathValue("category")`,
		"strconv.ParseInt(raw, 10, 0)",
		"httpapp.Order(r.Context(), req, s.app.repo)",
		// Lifecycle: both servers run, both stop, both report readiness.
		`servoimpl.Call{Node: "http", Fn: a.httpServer.run},`,
		`servoimpl.Call{Node: "http:telemetry", Fn: a.httpTelemetryServer.run},`,
		`return servoimpl.Serve(ctx, "http", s.srv, &s.ready)`,
		`return servoimpl.StopServer(ctx, "http:telemetry", s.srv, &s.ready, s.lc.ShutdownDelay, s.cancel)`,
		`Name: "http:telemetry"`,
		// The test seam: each group's wrapped handler, reachable without
		// binding a listener.
		"func (a *App) HTTPHandler(group string) http.Handler",
		// Graph() carries the HTTP plan as data, mirroring `servo graph`.
		"HTTP: &servo.GraphHTTP{",
		`{Method: "POST", Pattern: "/order/{category}/", Handler: "httpapp.Order", Args: []string{"*example.com/httpapp.Repo"}`,
		`{Type: "*example.com/httpapp.Guard", Scope: "server"}`,
		`{Type: "*example.com/httpapp.UserExtractor", Produces: "*example.com/httpapp.User"}`,
		`case "", "default":`,
		"return a.httpServer.srv.Handler",
		`case "telemetry":`,
		"return a.httpTelemetryServer.srv.Handler",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
	if t.Failed() {
		t.Logf("--- generated ---\n%s", src)
	}
}

// Wrap order is declaration order, outermost first: the server-level Guard
// must be applied AFTER the group-level Auth in the telemetry constructor,
// so it ends up outermost.
func TestEmitHTTPMiddlewareOrder(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	ctor := strings.Index(src, "func newHttpTelemetryServer(")
	if ctor < 0 {
		t.Fatalf("no telemetry constructor")
	}
	body := src[ctor:]
	end := strings.Index(body, "\n}\n")
	body = body[:end]
	auth := strings.Index(body, "a.auth.Middleware(handler)")
	guard := strings.Index(body, "a.guard.Middleware(handler)")
	if auth < 0 || guard < 0 || auth > guard {
		t.Fatalf("wrap order wrong (auth=%d guard=%d):\n%s", auth, guard, body)
	}
	// And the default group's constructor never sees the telemetry-only
	// Auth middleware.
	dctor := strings.Index(src, "func newHttpServer(")
	dbody := src[dctor:]
	dbody = dbody[:strings.Index(dbody, "\n}\n")]
	if strings.Contains(dbody, "auth.Middleware") {
		t.Fatalf("group middleware leaked into the default group:\n%s", dbody)
	}
}

// The server stops before every node, in every group.
func TestEmitHTTPShutdownStopsServersFirst(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	shutdownIdx := strings.Index(src, "func (a *App) Shutdown(")
	if shutdownIdx < 0 {
		t.Fatalf("no Shutdown in output")
	}
	body := src[shutdownIdx:]
	defStop := strings.Index(body, "a.stopHttpServer(ctx)")
	telStop := strings.Index(body, "a.stopHttpTelemetryServer(ctx)")
	repoStop := strings.Index(body, "a.stopRepo(ctx)")
	if defStop < 0 || telStop < 0 || repoStop < 0 {
		t.Fatalf("Shutdown missing stops (def=%d tel=%d repo=%d):\n%s", defStop, telStop, repoStop, body)
	}
	if defStop > repoStop || telStop > repoStop {
		t.Fatalf("servers stop after their dependencies:\n%s", body)
	}
}

// With several groups the first stop Shutdown reaches stops them all at
// once, so their ShutdownDelays overlap; every group's own stop method only
// returns what that recorded. The drain itself is servoimpl's, never the
// deprecated servo.RunStop.
func TestEmitHTTPStopsEveryGroupTogether(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	if !regexp.MustCompile(`\n\thttpServersStopOnce\s+sync\.Once\n`).MatchString(src) {
		t.Errorf("App has no shared httpServersStopOnce field")
	}
	for _, want := range []string{
		"func (a *App) stopHttpServers(ctx context.Context) {",
		"a.httpServersStopOnce.Do(func() {",
		"wg.Go(func() { a.httpServerStopResult = a.httpServer.stop(ctx) })",
		"wg.Go(func() { a.httpTelemetryServerStopResult = a.httpTelemetryServer.stop(ctx) })",
		// The nil receiver is the rollback reaching a server New never built.
		"func (s *httpTelemetryServer) stop(ctx context.Context) servo.NodeResult {\n\tif s == nil {",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
	for _, method := range []string{"stopHttpServer", "stopHttpTelemetryServer"} {
		start := strings.Index(src, "func (a *App) "+method+"(ctx context.Context) servo.NodeResult {")
		if start < 0 {
			t.Fatalf("no %s", method)
		}
		body := src[start : start+strings.Index(src[start:], "\n}\n")]
		if !strings.Contains(body, "a.stopHttpServers(ctx)") {
			t.Errorf("%s does not defer to the shared stop:\n%s", method, body)
		}
	}
	for _, unwanted := range []string{"httpServerStopOnce", "httpTelemetryServerStopOnce", "srv.Shutdown"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("generated source still has %q", unwanted)
		}
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "RunStop(") && (strings.Contains(line, `"http"`) || strings.Contains(line, `"http:`)) {
			t.Errorf("a server still stops through RunStop: %s", strings.TrimSpace(line))
		}
	}
	if t.Failed() {
		t.Logf("--- generated ---\n%s", src)
	}
}

// One group needs no shared stop: its method keeps the once-guarded shape
// every stoppable node has.
func TestEmitHTTPSingleGroupStop(t *testing.T) {
	resolved, spec := buildHTTPResolvedWith(t, func(in *resolve.Input, _ *types.Package) {
		in.HTTP.Groups = nil
		var uses []load.UseDecl
		for _, u := range in.HTTP.Uses {
			if len(u.Groups) == 0 {
				uses = append(uses, u)
			}
		}
		in.HTTP.Uses = uses
	})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	want := "func (a *App) stopHttpServer(ctx context.Context) servo.NodeResult {\n\ta.httpServerStopOnce.Do(func() { a.httpServerStopResult = a.httpServer.stop(ctx) })\n\treturn a.httpServerStopResult\n}"
	if !strings.Contains(src, want) {
		t.Errorf("generated source missing\n%s", want)
	}
	if strings.Contains(src, "stopHttpServers") || strings.Contains(src, "httpTelemetryServer") {
		t.Errorf("a single group still got the shared stop or a second server")
	}
	if t.Failed() {
		t.Logf("--- generated ---\n%s", src)
	}
}

// assertImportsBalanced fails for an import the file never uses, and for a
// package the HTTP code qualifies with but never imports — the emitter
// registers encoding/json, net/url, strconv and errors only for the routes
// that need them, and either mistake is a compile error in every app that
// declares servo.HTTP().
func assertImportsBalanced(t *testing.T, src string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "servo_gen.go", src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		t.Fatalf("parse imports: %v", err)
	}
	full, err := parser.ParseFile(token.NewFileSet(), "servo_gen.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	used := map[string]bool{}
	ast.Inspect(full, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	imported := map[string]bool{}
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		imported[name] = true
		if !used[name] {
			t.Errorf("imports %s without using it", p)
		}
	}
	for _, ident := range []string{"json", "url", "strconv", "errors", "servoimpl", "atomic", "sync", "http"} {
		if used[ident] && !imported[ident] {
			t.Errorf("qualifies with %s without importing it", ident)
		}
	}
}

func TestEmitHTTPImportsOnlyWhatItUses(t *testing.T) {
	t.Run("every feature", func(t *testing.T) {
		resolved, spec := buildHTTPResolved(t)
		for _, testMode := range []bool{false, true} {
			out, err := Emit(resolved, spec, testMode)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			assertImportsBalanced(t, string(out))
			if !strings.Contains(string(out), `"net/url"`) || !strings.Contains(string(out), `"encoding/json"`) {
				t.Errorf("a query route and a JSON body need net/url and encoding/json")
			}
		}
	})
	t.Run("a bare route", func(t *testing.T) {
		resolved, spec := buildHTTPResolvedWith(t, func(in *resolve.Input, _ *types.Package) {
			var routes []*route.Route
			for _, rt := range in.HTTP.Routes {
				if rt.Name == "httpapp.Health" {
					routes = append(routes, rt)
				}
			}
			in.HTTP.Routes = routes
			in.HTTP.Groups = nil
			in.HTTP.Uses = in.HTTP.Uses[:1] // Guard: server-wide, so it selects nothing that is gone
			in.HTTP.Extracts = nil
		})
		out, err := Emit(resolved, spec, false)
		if err != nil {
			t.Fatalf("Emit: %v", err)
		}
		src := string(out)
		assertImportsBalanced(t, src)
		for _, unwanted := range []string{`"net/url"`, `"encoding/json"`, `"strconv"`} {
			if strings.Contains(src, unwanted) {
				t.Errorf("a route with no request struct still imports %s", unwanted)
			}
		}
	})
}

// A plain func(http.Handler) http.Handler attaches as itself — called, not
// asked for a Middleware method — so otelhttp's or chi's middleware needs
// no wrapper type.
func TestEmitHTTPAcceptsPlainMiddlewareFuncs(t *testing.T) {
	var app *types.Package
	resolved, spec := buildHTTPResolvedWith(t, func(_ *resolve.Input, a *types.Package) { app = a })
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	if !strings.Contains(src, "handler = a.tag(handler)") {
		t.Errorf("the func middleware is not called as itself:\n%s", src)
	}
	if strings.Contains(src, "a.tag.Middleware(") {
		t.Errorf("the func middleware was asked for a Middleware method it does not have")
	}

	lookup := func(name string) types.Type { return app.Scope().Lookup(name).Type() }
	cases := []struct {
		name string
		t    types.Type
		want bool
	}{
		{"a defined func type", lookup("Tag"), true},
		{"an unnamed func type", lookup("Tag").Underlying(), true},
		{"a Middleware method", types.NewPointer(lookup("Guard")), false},
		{"a func type with a Middleware method", lookup("Explicit"), false},
	}
	for _, c := range cases {
		if got := httpMiddlewareIsFunc(c.t); got != c.want {
			t.Errorf("%s: httpMiddlewareIsFunc = %v, want %v", c.name, got, c.want)
		}
	}
}

// A body-less GET route must not read the request body at all.
func TestEmitHTTPBodylessRouteSkipsBodyDecode(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	start := strings.Index(src, "func (s *httpServer) handleHealth(")
	if start < 0 {
		t.Fatalf("no handleHealth adapter:\n%s", src)
	}
	end := strings.Index(src[start:], "\n}\n")
	adapter := src[start : start+end]
	if strings.Contains(adapter, "Decode") || strings.Contains(adapter, "MaxBytesReader") {
		t.Fatalf("handleHealth reads a body:\n%s", adapter)
	}
}

func TestEmitHTTPTestMode(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		"type testHttpServer struct",
		"func newTestHttpServer(a *TestApp) (*testHttpServer, error)",
		"type testHttpTelemetryServer struct",
		"func (a *TestApp) HTTPHandler(group string) http.Handler",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("test-mode output missing %q\n---\n%s", want, src)
		}
	}
}

func TestEmitHTTPDeterministic(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	first, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit (1st): %v", err)
	}
	second, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit (2nd): %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("Emit produced different output across two runs on identical input")
	}
}

func TestEmitHTTPGolden(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "httpapp.go.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("golden file updated")
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(want) != string(out) {
		t.Errorf("output does not match golden file %s (run with UPDATE_GOLDEN=1 to review+accept changes)\n--- got ---\n%s", goldenPath, out)
	}
}

// Extraction runs before the request is decoded: a rejected credential
// costs nothing, and a malformed body cannot turn a 401 into a 400.
func TestEmitHTTPExtractorsRunBeforeDecode(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	start := strings.Index(src, "func (s *httpServer) handleUpdateMe(")
	if start < 0 {
		t.Fatalf("no handleUpdateMe adapter:\n%s", src)
	}
	adapter := src[start:]
	adapter = adapter[:strings.Index(adapter, "\n}\n")]
	extract := strings.Index(adapter, "user, err := s.app.userExtractor.Extract(r)")
	decode := strings.Index(adapter, "json.NewDecoder(r.Body).Decode(req)")
	call := strings.Index(adapter, "httpapp.UpdateMe(r.Context(), req, user)")
	if extract < 0 || decode < 0 || call < 0 || extract > decode || decode > call {
		t.Fatalf("adapter order wrong (extract=%d decode=%d call=%d):\n%s", extract, decode, call, adapter)
	}
}

// The servers are wired after the last constructor and before the first
// Init, so a group missing its HTTPConfig.Groups entry fails before anything
// expensive has started — and unwinds the way an Init failure does.
func TestEmitHTTPServersBuiltBeforeInit(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	start := strings.Index(src, "func New(ctx context.Context) (*App, error) {")
	if start < 0 {
		t.Fatalf("no New:\n%s", src)
	}
	body := src[start:]
	body = body[:strings.Index(body, "\n}\n")]
	lastCtor := strings.Index(body, "repo := httpapp.NewRepo()")
	server := strings.Index(body, "httpServer, err := newHttpServer(a)")
	initCall := strings.Index(body, "Fn: a.repo.Init}")
	if lastCtor < 0 || server < 0 || initCall < 0 || lastCtor > server || server > initCall {
		t.Fatalf("New order wrong (ctor=%d server=%d init=%d):\n%s", lastCtor, server, initCall, body)
	}
	if !strings.Contains(body, "a.Shutdown(context.WithoutCancel(ctx))") || strings.Contains(body, "a.Shutdown(ctx)") {
		t.Fatalf("server setup must roll back on a context the signal cannot cancel:\n%s", body)
	}
}
