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
// package qualifier — the shape the tutorial's layered app has.
const httpAppSrc = `
package httpapp

import (
	"context"

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
`

const httpMainSrc = `
package main
`

func buildHTTPResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	fset := token.NewFileSet()
	importer := newPkgImporter(servoPkg)
	mod := &packages.Module{Path: "example.com", Main: true}

	parse := func(path, src string) (*packages.Package, *ast.File) {
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
		}, f
	}

	appPkg, _ := parse("example.com/httpapp", httpAppSrc)
	mainPkg, _ := parse("example.com/httpapp/cmd/app", httpMainSrc)
	pkgs := []*packages.Package{appPkg, mainPkg}

	candidates, _ := graph.ScanCandidates(pkgs, mainPkg.PkgPath)
	routes, rdiags := route.Scan(pkgs, servoPkg.Types)
	if len(rdiags) != 0 {
		t.Fatalf("fixture scan diagnostics: %v", rdiags)
	}

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
			Pos:        token.Position{Filename: "spec.go", Line: 11},
			Routes:     routes,
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
		// The server type, its constructor, and the App wiring at the very
		// end of New — after Init, since construction is pure wiring and
		// the listener binds in run().
		"type httpServer struct",
		"func newHttpServer(a *App) *httpServer",
		"a.httpServer = newHttpServer(a)",
		// Route registration in scan order, method prepended.
		`mux.HandleFunc("GET /health", s.handleHealth)`,
		`mux.HandleFunc("POST /notes", s.handleCreateNote)`,
		`mux.HandleFunc("POST /order/{category}/", s.handleOrder)`,
		// The listen address comes from the user's config node.
		"net.JoinHostPort(",
		// Typed request decoding: path and query with strconv, JSON body
		// under MaxBytesReader.
		`req.Category = r.PathValue("category")`,
		"strconv.ParseInt(raw, 10, 0)",
		"http.MaxBytesReader(w, r.Body,",
		// The handler call is package-qualified with graph-resolved deps.
		"httpapp.Order(r.Context(), req, s.app.repo)",
		// Status semantics: recover the HTTPStatus, 2xx-as-error succeeds,
		// 5xx logs and hides the detail.
		"errors.As(err, &hs)",
		"slog.Error(",
		`s.writeError(w, http.StatusInternalServerError, "Internal Server Error")`,
		// Lifecycle: single runner is the server itself; the stop method
		// shuts the listener down under the standard budget.
		"return a.httpServer.run(ctx)",
		`servo.RunStop(ctx, servo.DefaultStopBudget, "http", a.httpServer.srv.Shutdown)`,
		// TLS branch driven by config.
		"ServeTLS(ln,",
		// Ready gains the listener-bound entry.
		"a.httpServer.ready.Load()",
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

// The server is the pseudo-root: it must stop before every node, so its
// stop call is the first thing Shutdown appends.
func TestEmitHTTPShutdownStopsServerFirst(t *testing.T) {
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
	serverStop := strings.Index(body, "a.stopHttpServer(ctx)")
	repoStop := strings.Index(body, "a.stopRepo(ctx)")
	if serverStop < 0 || repoStop < 0 {
		t.Fatalf("Shutdown missing stops (server=%d repo=%d):\n%s", serverStop, repoStop, body)
	}
	if serverStop > repoStop {
		t.Fatalf("server stops after its dependencies:\n%s", body)
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
		"func newTestHttpServer(a *TestApp) *testHttpServer",
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
