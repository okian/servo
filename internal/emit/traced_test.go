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
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// tracedAppSrc covers every method shape the wrapper has to render: a
// context with an error result, with a value and an error, with results
// and no error, with no results, a variadic, unnamed parameters,
// parameters spelled like the wrapper's own locals, and methods with no
// context at all.
const tracedAppSrc = `
package app

import "context"

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }
func (l *Logger) Stop(ctx context.Context) error { return nil }

type missingError struct{}
func (missingError) Error() string { return "missing" }

// ErrMissing is the expected error: a miss is not a failure.
var ErrMissing error = missingError{}

type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Put(ctx context.Context, key, value string) error
	Keys(ctx context.Context, prefixes ...string) ([]string, error)
	Each(ctx context.Context, fn func(string)) int
	Touch(ctx context.Context)
	Scan(context.Context, int, bool) (string, bool, error)
	Mark(ctx context.Context, span string, err error) error
	Close() error
	Name() string
}

type DB struct{}
func (d *DB) Get(ctx context.Context, key string) (string, error) { return "", nil }
func (d *DB) Put(ctx context.Context, key, value string) error { return nil }
func (d *DB) Keys(ctx context.Context, prefixes ...string) ([]string, error) { return nil, nil }
func (d *DB) Each(ctx context.Context, fn func(string)) int { return 0 }
func (d *DB) Touch(ctx context.Context) {}
func (d *DB) Scan(ctx context.Context, n int, b bool) (string, bool, error) { return "", false, nil }
func (d *DB) Mark(ctx context.Context, span string, err error) error { return nil }
func (d *DB) Close() error { return nil }
func (d *DB) Name() string { return "db" }
func (d *DB) Init(ctx context.Context) error { return nil }
func (d *DB) Stop(ctx context.Context) error { return nil }
func NewDB(l *Logger) (*DB, error) { return &DB{}, nil }

// TracerProvider stands in for go.opentelemetry.io/otel/trace.TracerProvider
// as far as the graph is concerned; the wrapper itself is written against
// the real package's import path regardless.
type TracerProvider interface{ Tracer(name string) string }
type SDK struct{}
func (s *SDK) Tracer(name string) string { return name }
func (s *SDK) Stop(ctx context.Context) error { return nil }
func NewSDK() *SDK { return &SDK{} }

type Server struct{}
func (s *Server) Run(ctx context.Context) error { return nil }
func NewServer(st Store) *Server { return &Server{} }
`

func buildTracedAppResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", tracedAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	appPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }
	spec := &load.Spec{
		InjectorPkg: appPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}},
		Traces: []load.TraceDecl{{
			Iface: graph.NewKey(lookup("Store"), ""), IfaceType: lookup("Store"),
			Pos:      token.Position{Filename: "spec.go", Line: 10},
			Expected: []load.ExpectedErr{{Obj: pkg.Scope().Lookup("ErrMissing"), Pos: token.Position{Filename: "spec.go", Line: 10, Column: 20}}},
		}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/app": true},
		Trace:      &resolve.TraceInput{ProviderKey: graph.NewKey(lookup("TracerProvider"), ""), ProviderType: lookup("TracerProvider")},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

func TestEmitTracedGolden(t *testing.T) {
	resolved, spec := buildTracedAppResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "tracedapp.go.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
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

// TestEmitTracedWrapper pins the shape of the wrapper independently of
// the golden's exact bytes: each method shape, the delegation of
// context-less methods, the expected-error check, and the lifecycle
// staying on the implementation.
func TestEmitTracedWrapper(t *testing.T) {
	resolved, spec := buildTracedAppResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		`"go.opentelemetry.io/otel/codes"`,
		`"go.opentelemetry.io/otel/trace"`,
		"type tracedStore struct {\n\tinner  Store\n\ttracer trace.Tracer\n}",
		"func newTracedStore(inner Store, tp trace.TracerProvider) *tracedStore {\n\treturn &tracedStore{inner: inner, tracer: tp.Tracer(\"example.com/app\")}\n}",
		// A context plus a value and an error.
		"func (t *tracedStore) Get(ctx context.Context, key string) (string, error) {\n\tctx, span := t.tracer.Start(ctx, \"app.Store/Get\")\n\tdefer span.End()\n\tr0, err := t.inner.Get(ctx, key)\n\tif err != nil && !errors.Is(err, ErrMissing) {\n\t\tspan.RecordError(err)\n\t\tspan.SetStatus(codes.Error, err.Error())\n\t}\n\treturn r0, err\n}",
		// A context plus an error alone.
		"err := t.inner.Put(ctx, key, value)\n\tif err != nil && !errors.Is(err, ErrMissing) {",
		// A variadic.
		"func (t *tracedStore) Keys(ctx context.Context, prefixes ...string) ([]string, error) {",
		"r0, err := t.inner.Keys(ctx, prefixes...)",
		// Results but no error: delegated inside the span, nothing to inspect.
		"func (t *tracedStore) Each(ctx context.Context, fn func(string)) int {\n\tctx, span := t.tracer.Start(ctx, \"app.Store/Each\")\n\tdefer span.End()\n\treturn t.inner.Each(ctx, fn)\n}",
		// No results at all.
		"func (t *tracedStore) Touch(ctx context.Context) {\n\tctx, span := t.tracer.Start(ctx, \"app.Store/Touch\")\n\tdefer span.End()\n\tt.inner.Touch(ctx)\n}",
		// Unnamed parameters get positional names; three results.
		"func (t *tracedStore) Scan(ctx context.Context, p1 int, p2 bool) (string, bool, error) {",
		"r0, r1, err := t.inner.Scan(ctx, p1, p2)",
		"return r0, r1, err",
		// Parameters spelled like the body's own locals are renamed.
		"func (t *tracedStore) Mark(ctx context.Context, p1 string, p2 error) error {",
		// No context: delegated untouched.
		"func (t *tracedStore) Close() error {\n\treturn t.inner.Close()\n}",
		"func (t *tracedStore) Name() string {\n\treturn t.inner.Name()\n}",
		// Construction, after both dependencies, and the consumer takes it.
		"storeTraced := newTracedStore(db, sdk)\n\ta.storeTraced = storeTraced",
		"server := NewServer(storeTraced)",
		// The implementation keeps its lifecycle; the wrapper has none.
		"a.db.Init(ctx)",
		"func (a *App) stopDb(ctx context.Context) servo.NodeResult",
		`Binding: "traced"`,
		`Wraps: "*example.com/app.DB"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}
	if !regexp.MustCompile(`\n\tstoreTraced\s+\*tracedStore\n`).MatchString(src) {
		t.Errorf("App struct is missing the wrapper field:\n%s", src)
	}
	for _, unwanted := range []string{"stopStoreTraced", "a.storeTraced.Init", "a.storeTraced.Stop", "a.storeTraced.Close"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("generated source must not contain %q: the wrapper has no lifecycle of its own\n---\n%s", unwanted, src)
		}
	}
}

// TestEmitTracedTestVariantPrefixesTypes: the override variant lands in
// the same package as the production file, so its wrapper types need
// names of their own, exactly as a scope's or a server's do.
func TestEmitTracedTestVariantPrefixesTypes(t *testing.T) {
	resolved, spec := buildTracedAppResolved(t)
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		"type testTracedStore struct",
		"func newTestTracedStore(inner Store, tp trace.TracerProvider) *testTracedStore",
		"storeTraced := newTestTracedStore(db, sdk)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("test-variant source missing %q\n---\n%s", want, src)
		}
	}
	if strings.Contains(src, "type tracedStore struct") {
		t.Errorf("test-variant source redeclares the production wrapper type:\n%s", src)
	}
}

// otherSrc is a second package sharing the interface name Store, with its
// own expected error: the two wrappers' fields have to be qualified apart,
// the errors.Is check has to name the sentinel package-qualified, and a
// Trace with no Expected at all is the other branch of the doc comment.
const otherSrc = `
package other

import "context"

type missing struct{}
func (missing) Error() string { return "other: missing" }

var ErrMissing error = missing{}

type Store interface {
	Lookup(ctx context.Context, key string) (string, error)
	Reset()
}
`

const twoPackageAppSrc = `
package app

import (
	"context"

	"example.com/other"
)

type DB struct{}
func (d *DB) Get(ctx context.Context, key string) (string, error) { return "", nil }
func (d *DB) Lookup(ctx context.Context, key string) (string, error) { return "", nil }
func (d *DB) Reset() {}
func NewDB() *DB { return &DB{} }

type Store interface {
	Get(ctx context.Context, key string) (string, error)
}

type TracerProvider interface{ Tracer(name string) string }
type SDK struct{}
func (s *SDK) Tracer(name string) string { return name }
func NewSDK() *SDK { return &SDK{} }

type Server struct{}
func NewServer(st Store) *Server { return &Server{} }

type Auditor struct{}
func NewAuditor(st other.Store) *Auditor { return &Auditor{} }
`

func buildTwoPackageTracedResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	importer := newPkgImporter(servoPkg)
	fset := token.NewFileSet()

	otherFile, err := parser.ParseFile(fset, "other.go", otherSrc, 0)
	if err != nil {
		t.Fatalf("parse other: %v", err)
	}
	otherPkg, err := (&types.Config{Importer: importer}).Check("example.com/other", fset, []*ast.File{otherFile}, nil)
	if err != nil {
		t.Fatalf("typecheck other: %v", err)
	}
	importer.byPath["example.com/other"] = otherPkg

	appFile, err := parser.ParseFile(fset, "app.go", twoPackageAppSrc, 0)
	if err != nil {
		t.Fatalf("parse app: %v", err)
	}
	pkg, err := (&types.Config{Importer: importer}).Check("example.com/app", fset, []*ast.File{appFile}, nil)
	if err != nil {
		t.Fatalf("typecheck app: %v", err)
	}
	appPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(p *types.Package, name string) types.Type { return p.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(pkg, name)) }
	spec := &load.Spec{
		InjectorPkg: appPkg,
		Roots: []load.RootDecl{
			{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 9}},
			{Key: graph.NewKey(ptr("Auditor"), ""), Type: ptr("Auditor"), Pos: token.Position{Filename: "spec.go", Line: 10}},
		},
		Traces: []load.TraceDecl{
			{Iface: graph.NewKey(lookup(pkg, "Store"), ""), IfaceType: lookup(pkg, "Store"), Pos: token.Position{Filename: "spec.go", Line: 11}},
			{
				Iface: graph.NewKey(lookup(otherPkg, "Store"), ""), IfaceType: lookup(otherPkg, "Store"),
				Pos:      token.Position{Filename: "spec.go", Line: 12},
				Expected: []load.ExpectedErr{{Obj: otherPkg.Scope().Lookup("ErrMissing"), Pos: token.Position{Filename: "spec.go", Line: 12, Column: 30}}},
			},
		},
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/app": true},
		Trace:      &resolve.TraceInput{ProviderKey: graph.NewKey(lookup(pkg, "TracerProvider"), ""), ProviderType: lookup(pkg, "TracerProvider")},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

// TestEmitTwoTracedInterfacesWithOneName pins the cases the golden
// fixture cannot hold: two traced interfaces sharing a short name, an
// expected error from another package, a wrapper with no Expected at
// all, and a context-less method with no results.
func TestEmitTwoTracedInterfacesWithOneName(t *testing.T) {
	resolved, spec := buildTwoPackageTracedResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		// Both fields qualified by package, both types distinct.
		"appStoreTraced := newTracedStore(db, sdk)",
		"otherStoreTraced := newTracedStore2(db, sdk)",
		"server := NewServer(appStoreTraced)",
		"auditor := NewAuditor(otherStoreTraced)",
		"type tracedStore2 struct {\n\tinner  other.Store\n\ttracer trace.Tracer\n}",
		`tp.Tracer("example.com/other")`,
		// The foreign sentinel, package-qualified, in the check and the comment.
		"if err != nil && !errors.Is(err, other.ErrMissing) {",
		"span — except other.ErrMissing, declared expected.",
		// No Expected on the first wrapper: the plain check and the plain comment.
		"r0, err := t.inner.Get(ctx, key)\n\tif err != nil {\n\t\tspan.RecordError(err)",
		"span. Methods without a\n// context are delegated untouched",
		// Context-less, result-less: a bare delegation.
		"func (t *tracedStore2) Reset() {\n\tt.inner.Reset()\n}",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}
}

// TestTracedTypeNameFallsBackForAnUnnamedInterface: the loader never lets
// an interface literal through, so the fallback exists only to keep the
// emitter total.
func TestTracedTypeNameFallsBackForAnUnnamedInterface(t *testing.T) {
	if got := tracedTypeName(types.NewInterfaceType(nil, nil)); got != "Iface" {
		t.Fatalf("tracedTypeName(interface{}) = %q, want Iface", got)
	}
}
