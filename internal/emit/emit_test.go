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
)

// pkgImporter and loadServoPackage let a fixture's types.Config.Check share
// object identity (for "context.Context" etc.) with the real servo package
// loaded via go/packages — without this, types.Implements silently fails
// on every capability with a context.Context parameter. See
// internal/graph/capabilities_test.go, which hit and documented this first.
type pkgImporter struct{ byPath map[string]*types.Package }

func newPkgImporter(roots ...*packages.Package) *pkgImporter {
	idx := &pkgImporter{byPath: map[string]*types.Package{}}
	var add func(p *packages.Package)
	add = func(p *packages.Package) {
		if _, ok := idx.byPath[p.PkgPath]; ok {
			return
		}
		idx.byPath[p.PkgPath] = p.Types
		for _, dep := range p.Imports {
			add(dep)
		}
	}
	for _, p := range roots {
		add(p)
	}
	return idx
}

func (i *pkgImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if pkg, ok := i.byPath[path]; ok {
		return pkg, nil
	}
	return nil, &importNotFoundError{path}
}

type importNotFoundError struct{ path string }

func (e *importNotFoundError) Error() string { return "pkgImporter: package not found: " + e.path }

func loadServoPackage(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, graph.ServoPackagePath)
	if err != nil {
		t.Fatalf("load servo package: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("expected exactly one loaded servo package, got %d", len(pkgs))
	}
	return pkgs[0]
}

const fullAppSrc = `
package app

import "context"

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }
func (l *Logger) Stop(ctx context.Context) error { return nil }

type Store interface{ Get(key string) string }

type DB struct{}
func (d *DB) Get(key string) string { return "" }
func (d *DB) Init(ctx context.Context) error { return nil }
func (d *DB) Stop(ctx context.Context) error { return nil }
func (d *DB) Health(ctx context.Context) error { return nil }
func NewDB(l *Logger) (*DB, func(), error) { return &DB{}, func() {}, nil }

type Cache struct{}
func (c *Cache) Init(ctx context.Context) error { return nil }
func NewCache(l *Logger) *Cache { return &Cache{} }

type Server struct{}
func (s *Server) Init(ctx context.Context) error { return nil }
func (s *Server) Run(ctx context.Context) error { return nil }
func (s *Server) Drain(ctx context.Context) error { return nil }
func (s *Server) Stop(ctx context.Context) error { return nil }
func (s *Server) Ready(ctx context.Context) error { return nil }
func NewServer(st Store, c *Cache) *Server { return &Server{} }

type Worker struct{}
func (w *Worker) Run(ctx context.Context) error { return nil }
func NewWorker(l *Logger) *Worker { return &Worker{} }
`

func checkFullAppFixture(t *testing.T, servoPkg *packages.Package) (*packages.Package, []*graph.Provider) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", fullAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	accepted, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/app")
	return pkgsPkg, accepted
}

func buildFullAppResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	appPkg, candidates := checkFullAppFixture(t, servoPkg)
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(name string) types.Type { return appPkg.Types.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }

	spec := &load.Spec{
		InjectorPkg: appPkg,
		Roots: []load.RootDecl{
			{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 9}},
			{Key: graph.NewKey(ptr("Worker"), ""), Type: ptr("Worker"), Pos: token.Position{Filename: "spec.go", Line: 10}},
		},
		Binds: []load.BindDecl{
			{Iface: graph.NewKey(lookup("Store"), ""), IfaceType: lookup("Store"), Concrete: graph.NewKey(ptr("DB"), ""), ConcreteType: ptr("DB")},
		},
	}

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/app": true},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

func TestEmitFullApp(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	wantSubstrings := []string{
		"//go:build !servoinject",
		"func New(ctx context.Context) (*App, error)",
		"func (a *App) Run(ctx context.Context) error",
		"func (a *App) Shutdown(ctx context.Context) servo.Report",
		"func (a *App) Health(ctx context.Context) servo.Report",
		"func (a *App) Ready(ctx context.Context) servo.Report",
		"func (a *App) Graph() servo.Graph",
		"func (a *App) Report() servo.StartupReport",
		"db, dbCleanup, err := NewDB(logger)",                     // construction with cleanup + error
		"_ = a.stopLogger(ctx)",                                   // rollback stops the one prior stoppable node
		"errgroup.WithContext(ctx)",                               // both the concurrent Init level and the 2-runner Run need this
		"a.db.Health(ctx)",                                        // Health only iterates Healther nodes
		"a.server.Ready(ctx)",                                     // Ready only iterates Readier nodes
		"signal.Notify(forceExit, os.Interrupt, syscall.SIGTERM)", // double-signal force-exit watcher
		"servo.RunStop(ctx, servo.DefaultStopBudget, \"*example.com/app.Server\", a.server.Drain)",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}

	// Worker has no Init and is not part of any errgroup Init level, and
	// Cache has no stop-worthy capability, so it must not get a stop method.
	if strings.Contains(src, "a.worker.Init") {
		t.Error("Worker has no Initializer capability and must not be Init'd")
	}
	if strings.Contains(src, "func (a *App) stopCache(") {
		t.Error("Cache has no Drain/Flush/Finalizer/cleanup and must not get a stop method")
	}
}

func TestEmitDeterministic(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)
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

func TestEmitTestModeUsesTestAppType(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	// TestApp, not App: an override can resolve an entirely different
	// concrete type for the same interface key, so the two variants can't
	// safely share a struct (different fields, different capabilities).
	if !strings.Contains(src, "func NewTestApp(ctx context.Context) (*TestApp, error)") {
		t.Errorf("test-mode output missing the NewTestApp constructor returning *TestApp:\n%s", src)
	}
	if !strings.Contains(src, "type TestApp struct") {
		t.Errorf("test-mode output missing `type TestApp struct`:\n%s", src)
	}
	if strings.Contains(src, "*App)") || strings.Contains(src, "*App{") {
		t.Errorf("test-mode output must not reference the production App type:\n%s", src)
	}
}

// TestEmitGolden compares against a checked-in file, refreshed by running
// with UPDATE_GOLDEN=1. No separate generic golden-harness package exists
// since emit is its only consumer.
func TestEmitGolden(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "fullapp.go.golden")
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

// TestEmitTrivialGraphHasNoUnusedImports guards the degenerate case: a
// single leaf node with no capabilities and no error path at all, where
// "sync"/"time"/"errors"/"errgroup" must never be registered — Go treats
// an unused import as a compile error, and this is the shape most likely
// to trip that if any import registration were unconditional instead of
// tied to actual use.
func TestEmitTrivialGraphHasNoUnusedImports(t *testing.T) {
	const src = `
package app
type Leaf struct{}
func NewLeaf() *Leaf { return &Leaf{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/trivial", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/trivial", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/trivial")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	leafPtr := types.NewPointer(pkg.Scope().Lookup("Leaf").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(leafPtr, ""), Type: leafPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/trivial": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, unused := range []string{`"sync"`, `"time"`, `"errors"`, `"golang.org/x/sync/errgroup"`} {
		if strings.Contains(string(out), unused) {
			t.Errorf("trivial graph must not import %s (nothing uses it, and Go treats unused imports as compile errors):\n%s", unused, out)
		}
	}
}

// TestEmitUnexportedConstructorInInjectorPackage covers a root provided by
// an unexported constructor living in the injector's own package —
// internal/graph/scan_test.go only confirms such a function is scanned as
// a candidate; this exercises it end to end through Resolve and Emit,
// where the generated call site must reference it bare (unqualified),
// since the generated file lives in that same package and an unexported
// identifier can't be package-qualified from outside it anyway.
func TestEmitUnexportedConstructorInInjectorPackage(t *testing.T) {
	const src = `
package app
type Leaf struct{}
func newLeaf() *Leaf { return &Leaf{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/unexported", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/unexported", Types: pkg, Fset: fset}
	// injectorPkgPath == this package, so newLeaf is a legal candidate.
	candidates, rejected := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/unexported")
	for _, r := range rejected {
		if r.Name == "app.newLeaf" {
			t.Fatalf("newLeaf must be accepted, not rejected, when scanned as its own injector package: %v", r)
		}
	}
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	leafPtr := types.NewPointer(pkg.Scope().Lookup("Leaf").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(leafPtr, ""), Type: leafPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/unexported": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	if !strings.Contains(src2, "leaf := newLeaf()") {
		t.Errorf("expected a bare, unqualified call to newLeaf, got:\n%s", src2)
	}
	if strings.Contains(src2, "app.newLeaf") {
		t.Errorf("an unexported identifier can never be package-qualified, got:\n%s", src2)
	}
}

// TestEmitZeroRootSpec covers servo.Build() with no Root() calls at all —
// an empty root set. Resolve must not panic on an empty Spec.Roots loop,
// and Emit must still produce a valid, if useless, App with a working but
// entirely empty New/Run/Shutdown.
func TestEmitZeroRootSpec(t *testing.T) {
	const src = `
package app
type Leaf struct{}
func NewLeaf() *Leaf { return &Leaf{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/zeroroot", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/zeroroot", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/zeroroot")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	spec := &load.Spec{InjectorPkg: pkgsPkg, Roots: nil}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/zeroroot": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics for an empty root set: %v", diags)
	}
	if len(resolved.Order) != 0 || len(resolved.Roots) != 0 {
		t.Fatalf("expected an empty graph, got %d nodes / %d roots", len(resolved.Order), len(resolved.Roots))
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit on a zero-root graph: %v", err)
	}
	src2 := string(out)
	for _, want := range []string{
		"type App struct",
		"func New(ctx context.Context) (*App, error)",
		"func (a *App) Run(ctx context.Context) error",
		"func (a *App) Shutdown(ctx context.Context) servo.Report",
	} {
		if !strings.Contains(src2, want) {
			t.Errorf("zero-root output missing %q:\n%s", want, src2)
		}
	}
	// Leaf is a real candidate but unreachable from any root, so it must
	// never appear in the emitted output at all.
	if strings.Contains(src2, "Leaf") {
		t.Errorf("unreachable candidate Leaf must not appear in zero-root output:\n%s", src2)
	}
}

// TestEmitTypeNameThatIsAGoKeyword guards a real bug class: a type whose
// name lowercases to a Go keyword ("Range" -> "range") must not produce a
// bare `range := ...` declaration, a syntax error format.Source would
// reject. Emit returning no error already proves the output parses; this
// additionally confirms the identifier itself was renamed, not just that
// gofmt happened to tolerate it.
func TestEmitTypeNameThatIsAGoKeyword(t *testing.T) {
	const src = `
package app
type Range struct{}
func NewRange() *Range { return &Range{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/keyword", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/keyword", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/keyword")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	rangePtr := types.NewPointer(pkg.Scope().Lookup("Range").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(rangePtr, ""), Type: rangePtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/keyword": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v (a bare `range :=` would fail format.Source)", err)
	}
	if !strings.Contains(string(out), "range2 := ") {
		t.Errorf("expected the keyword-colliding variable to be named range2, got:\n%s", out)
	}
}

// TestEmitReturnsErrorWhenGeneratedSourceFailsToFormat covers Emit's own
// format.Source failure path directly: nothing in a normal spec/resolved
// graph can produce syntactically invalid Go (every identifier involved
// already passed type-checking), so this reaches the branch the way it
// would actually fire in practice — a servo bug feeding a raw, unvalidated
// package name into the template — rather than through the real pipeline.
func TestEmitReturnsErrorWhenGeneratedSourceFailsToFormat(t *testing.T) {
	spec := &load.Spec{InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/bad"}} // "type" is a keyword, illegal as a package name
	_, err := Emit(&resolve.Resolved{}, spec, false)
	if err == nil || !strings.Contains(err.Error(), "failed to format") {
		t.Fatalf("got err=%v, want a 'failed to format' error", err)
	}
}

// TestEmitRelativizesPositionsAgainstModuleDir covers posString's rewrite
// branch: every other test's fixture leaves InjectorPkg.Module nil, so the
// generated header always fell through to the raw, un-relativized
// position. A real module sets Module.Dir, and the header comment must
// name each provider's file relative to it rather than embedding one
// checkout's absolute path in output meant to be committed to VCS.
func TestEmitRelativizesPositionsAgainstModuleDir(t *testing.T) {
	const src = `
package app
type Leaf struct{}
func NewLeaf() *Leaf { return &Leaf{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "/repo/app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/modrel", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/modrel", Types: pkg, Fset: fset, Module: &packages.Module{Dir: "/repo"}}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/modrel")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	leafPtr := types.NewPointer(pkg.Scope().Lookup("Leaf").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(leafPtr, ""), Type: leafPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/modrel": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	if strings.Contains(src2, "/repo/app.go") {
		t.Errorf("expected the module-relative path, not the absolute one, got:\n%s", src2)
	}
	if !strings.Contains(src2, "app.go:") {
		t.Errorf("expected the relativized filename app.go to still appear, got:\n%s", src2)
	}
}

// TestEmitQualifiesCrossPackageAndGenericTypes covers qualifiedTypeString's
// remaining branches, none reachable through fullAppSrc (a single
// self-contained package with no generic types): a *types.Named from a
// different package (requiring import qualification), a generic type's
// instantiated arguments (basic, empty-interface, and non-empty anonymous
// interface), and — since a qualified reference inside an anonymous
// interface's method signature routes through go/types' own
// types.TypeString — the qualifierFunc closure itself.
func TestEmitQualifiesCrossPackageAndGenericTypes(t *testing.T) {
	const depSrc = `
package dep
type Box[T any] struct{ V T }
type Marker struct{}
`
	const appSrc = `
package app
import "example.com/dep"
func NewBoxFunc() *dep.Box[func() dep.Marker] { return &dep.Box[func() dep.Marker]{} }
func NewBoxAny() *dep.Box[any] { return &dep.Box[any]{} }
func NewBoxIface() *dep.Box[interface{ M() dep.Marker }] { return &dep.Box[interface{ M() dep.Marker }]{} }
`
	servoPkg := loadServoPackage(t)
	importer := newPkgImporter(servoPkg)

	depFset := token.NewFileSet()
	depFile, err := parser.ParseFile(depFset, "dep.go", depSrc, 0)
	if err != nil {
		t.Fatalf("parse dep: %v", err)
	}
	depPkg, err := (&types.Config{Importer: importer}).Check("example.com/dep", depFset, []*ast.File{depFile}, nil)
	if err != nil {
		t.Fatalf("typecheck dep: %v", err)
	}
	importer.byPath["example.com/dep"] = depPkg

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", appSrc, 0)
	if err != nil {
		t.Fatalf("parse app: %v", err)
	}
	pkg, err := (&types.Config{Importer: importer}).Check("example.com/genericapp", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck app: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/genericapp", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/genericapp")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	root := func(name string) load.RootDecl {
		t := types.NewPointer(pkg.Scope().Lookup(name).Type().(*types.Signature).Results().At(0).Type().(*types.Pointer).Elem())
		_ = t
		fn := pkg.Scope().Lookup(name).(*types.Func)
		resultType := fn.Type().(*types.Signature).Results().At(0).Type()
		return load.RootDecl{Key: graph.NewKey(resultType, ""), Type: resultType, Pos: token.Position{Filename: "spec.go", Line: 5}}
	}
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{root("NewBoxFunc"), root("NewBoxAny"), root("NewBoxIface")},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/genericapp": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	for _, want := range []string{
		`*dep.Box[func() dep.Marker]`,
		`*dep.Box[any]`,
		`*dep.Box[interface{ M() dep.Marker }]`,
		`"example.com/dep"`,
	} {
		if !strings.Contains(src2, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src2)
		}
	}
}

// TestEmitCleanupOnlyAndErrorOnlyConstructionShapes covers the two
// writeConstruction shapes fullAppSrc never exercises alone (its only
// error-returning provider, NewDB, also has cleanup) — cleanup without
// error, and error without cleanup — plus writeConstructionRollback's
// continue: ErrorOnly's rollback walks back over CleanupOnly (stoppable,
// gets a rollback line) and then Leaf (not stoppable, must be skipped
// rather than emitting a call to a stop method Leaf doesn't have).
func TestEmitCleanupOnlyAndErrorOnlyConstructionShapes(t *testing.T) {
	const src = `
package app
type Leaf struct{}
func NewLeaf() *Leaf { return &Leaf{} }

type CleanupOnly struct{}
func NewCleanupOnly(l *Leaf) (*CleanupOnly, func()) { return &CleanupOnly{}, func() {} }

type ErrorOnly struct{}
func NewErrorOnly(c *CleanupOnly) (*ErrorOnly, error) { return &ErrorOnly{}, nil }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/shapes", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/shapes", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/shapes")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	errorOnlyPtr := types.NewPointer(pkg.Scope().Lookup("ErrorOnly").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(errorOnlyPtr, ""), Type: errorOnlyPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/shapes": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	if !strings.Contains(src2, "cleanupOnly, cleanupOnlyCleanup := NewCleanupOnly(leaf)") {
		t.Errorf("missing the cleanup-without-error construction shape:\n%s", src2)
	}
	if !strings.Contains(src2, "errorOnly, err := NewErrorOnly(cleanupOnly)") {
		t.Errorf("missing the error-without-cleanup construction shape:\n%s", src2)
	}
	if !strings.Contains(src2, "_ = a.stopCleanupOnly(ctx)") {
		t.Errorf("expected ErrorOnly's rollback to stop the earlier, stoppable CleanupOnly:\n%s", src2)
	}
	if strings.Contains(src2, "stopLeaf") {
		t.Errorf("Leaf has no cleanup and must never get a stop method or a rollback call:\n%s", src2)
	}
}

// TestEmitFlusherCapabilityAndSingleRunner covers writeStopMethod's Flusher
// branch (fullAppSrc only exercises Drainer, Finalizer, and cleanup) and
// runFunc's exactly-one-Runner shortcut (fullAppSrc has two Runners, so it
// only ever exercises the errgroup path).
func TestEmitFlusherCapabilityAndSingleRunner(t *testing.T) {
	const src = `
package app
import "context"

type Flusher struct{}
func (f *Flusher) Flush(ctx context.Context) error { return nil }
func NewFlusher() *Flusher { return &Flusher{} }

type Solo struct{}
func (s *Solo) Run(ctx context.Context) error { return nil }
func NewSolo() *Solo { return &Solo{} }
`
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/soloflush", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/soloflush", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/soloflush")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	flusherPtr := types.NewPointer(pkg.Scope().Lookup("Flusher").Type())
	soloPtr := types.NewPointer(pkg.Scope().Lookup("Solo").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots: []load.RootDecl{
			{Key: graph.NewKey(flusherPtr, ""), Type: flusherPtr, Pos: token.Position{Filename: "spec.go", Line: 5}},
			{Key: graph.NewKey(soloPtr, ""), Type: soloPtr, Pos: token.Position{Filename: "spec.go", Line: 6}},
		},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/soloflush": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	if !strings.Contains(src2, "servo.RunStop(ctx, servo.DefaultStopBudget, \"*example.com/soloflush.Flusher\", a.flusher.Flush)") {
		t.Errorf("missing the Flusher stop call:\n%s", src2)
	}
	if !strings.Contains(src2, "return a.solo.Run(ctx)") {
		t.Errorf("expected the single-runner shortcut (a bare call, no errgroup):\n%s", src2)
	}
	if strings.Contains(src2, "errgroup") {
		t.Errorf("a single Runner must not pull in errgroup at all:\n%s", src2)
	}
}

// TestEmitQualifiesConstructorCallFromForeignPackage covers
// qualifiedFuncString's ident != "" branch: every other fixture's root
// provider is declared in the injector's own package (self-import is
// illegal, so that call site is always bare), but a dependency reached
// through a foreign package's exported constructor must be called
// qualified — dep.NewWidget(), not a bare NewWidget() that wouldn't even
// resolve.
func TestEmitQualifiesConstructorCallFromForeignPackage(t *testing.T) {
	const depSrc = `
package dep
type Widget struct{}
func NewWidget() *Widget { return &Widget{} }
`
	const appSrc = `
package app
import "example.com/dep"
type Holder struct{}
func NewHolder(w *dep.Widget) *Holder { return &Holder{} }
`
	servoPkg := loadServoPackage(t)
	importer := newPkgImporter(servoPkg)

	depFset := token.NewFileSet()
	depFile, err := parser.ParseFile(depFset, "dep.go", depSrc, 0)
	if err != nil {
		t.Fatalf("parse dep: %v", err)
	}
	depPkg, err := (&types.Config{Importer: importer}).Check("example.com/dep", depFset, []*ast.File{depFile}, nil)
	if err != nil {
		t.Fatalf("typecheck dep: %v", err)
	}
	importer.byPath["example.com/dep"] = depPkg
	depPkgsPkg := &packages.Package{Name: "dep", PkgPath: "example.com/dep", Types: depPkg, Fset: depFset}
	depCandidates, _ := graph.ScanCandidates([]*packages.Package{depPkgsPkg}, "example.com/foreignholder")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", appSrc, 0)
	if err != nil {
		t.Fatalf("parse app: %v", err)
	}
	pkg, err := (&types.Config{Importer: importer}).Check("example.com/foreignholder", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck app: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/foreignholder", Types: pkg, Fset: fset}
	appCandidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/foreignholder")
	candidates := append(appCandidates, depCandidates...)
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	holderPtr := types.NewPointer(pkg.Scope().Lookup("Holder").Type())
	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(holderPtr, ""), Type: holderPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: map[string]bool{"example.com/foreignholder": true, "example.com/dep": true}})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src2 := string(out)
	if !strings.Contains(src2, "widget := dep.NewWidget()") {
		t.Errorf("expected a package-qualified constructor call dep.NewWidget(), got:\n%s", src2)
	}
}
