package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
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
// extracted parameter, and middleware at all three levels.
const httpAppSrc = `
package httpapp

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type Repo struct{}
func NewRepo() *Repo { return &Repo{} }
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

type Guard struct{}
func NewGuard() *Guard { return &Guard{} }
func (m *Guard) Middleware(next http.Handler) http.Handler { return next }

type Auth struct{}
func NewAuth() *Auth { return &Auth{} }
func (m *Auth) Middleware(next http.Handler) http.Handler { return next }

type Audit struct{}
func NewAudit() *Audit { return &Audit{} }
func (m *Audit) Middleware(next http.Handler) http.Handler { return next }
`

const httpMainSrc = `
package main
`

func loadStdPkg(t *testing.T, path string) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, path)
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("load %s: %v", path, err)
	}
	return pkgs[0]
}

func buildHTTPResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
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
	resolved, diags := resolve.Resolve(resolve.Input{
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
			},
			Extracts:   []load.ExtractDecl{{Type: key("UserExtractor"), TypeT: ptr("UserExtractor"), Pos: token.Position{Filename: "spec.go", Line: 16}}},
			ConfigKey:  ck,
			ConfigType: ct,
		},
	})
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
		// One server type per group, each constructed fallibly at the end
		// of New.
		"type httpServer struct",
		"type httpTelemetryServer struct",
		"func newHttpServer(a *App) (*httpServer, error)",
		"func newHttpTelemetryServer(a *App) (*httpTelemetryServer, error)",
		"a.httpServer = httpServer",
		"a.httpTelemetryServer = httpTelemetryServer",
		// The default group reads the flat config fields; a named group
		// looks itself up and fails construction when missing.
		`lc, ok := cfg.Groups["telemetry"]`,
		`missing from HTTPConfig.Groups`,
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
		`httpWriteFailure(w, "httpapp.Me", "GET /me", err)`,
		"httpapp.Me(r.Context(), user)",
		// The shared helpers exist once, package-level.
		"func httpWriteFailure(w http.ResponseWriter, handler, route string, err error)",
		`httpWriteError(w, http.StatusInternalServerError, "Internal Server Error")`,
		"func httpRespond[T any](w http.ResponseWriter, code int, res servo.Json[T])",
		// Typed decode and the handler call are unchanged in spirit.
		`req.Category = r.PathValue("category")`,
		"strconv.ParseInt(raw, 10, 0)",
		"httpapp.Order(r.Context(), req, s.app.repo)",
		// Lifecycle: both servers run, both stop, both report readiness.
		"g.Go(func() error { return a.httpServer.run(gctx) })",
		"g.Go(func() error { return a.httpTelemetryServer.run(gctx) })",
		`servo.RunStop(ctx, servo.DefaultStopBudget, "http", a.httpServer.srv.Shutdown)`,
		`servo.RunStop(ctx, servo.DefaultStopBudget, "http:telemetry", a.httpTelemetryServer.srv.Shutdown)`,
		`Name: "http:telemetry"`,
		"ServeTLS(ln,",
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
