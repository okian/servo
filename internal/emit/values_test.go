package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// valuesAppSrc is a graph with things no constructor can produce: a flag
// struct parsed from the command line and a target read out of it. Both
// are declared with servo.Value, which makes them nodes resolved by type
// like any other while leaving them without a provider to call.
const valuesAppSrc = `
package app

import "context"

type Flags struct{ DSN string }

type Target int

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

type Store struct{}
func (s *Store) Init(ctx context.Context) error { return nil }
func NewStore(f Flags, l *Logger) *Store { return &Store{} }

type Migrator struct{}
func NewMigrator(s *Store, t Target) *Migrator { return &Migrator{} }
`

// buildValuesResolved resolves valuesAppSrc with root as the single
// servo.Root and each name in values declared as a servo.Value.
func buildValuesResolved(t *testing.T, root string, values ...string) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", valuesAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/values", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/values", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/values")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	rootPtr := types.NewPointer(lookup(root))

	spec := &load.Spec{
		InjectorPkg: pkgsPkg,
		Roots:       []load.RootDecl{{Key: graph.NewKey(rootPtr, ""), Type: rootPtr, Pos: token.Position{Filename: "spec.go", Line: 5, Column: 3}}},
	}
	for i, name := range values {
		vt := lookup(name)
		spec.Values = append(spec.Values, load.ValueDecl{
			Key: graph.NewKey(vt, ""), Type: vt,
			Pos: token.Position{Filename: "spec.go", Line: 6 + i, Column: 3},
		})
	}

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/values": true},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

// TestEmitSuppliedValueGetsAValuesStructAndAWithConstructor is the whole
// shape of servo.Value in one place: the caller writes a struct literal,
// hands it to NewWith, and the value is then indistinguishable from a
// constructed node for the rest of the file — same App field, same local,
// so every consumer's argument list is written the way it always was.
func TestEmitSuppliedValueGetsAValuesStructAndAWithConstructor(t *testing.T) {
	resolved, spec := buildValuesResolved(t, "Store", "Flags")

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	wantSubstrings := []string{
		// The struct the caller fills in. Its field is exported and named
		// after the type, not after the App's lowercase field.
		"type Values struct {\n\tFlags Flags\n}",
		// NewWith takes it. The parameter is `v`, which is why Emit
		// reserves that identifier as soon as a value is declared.
		"func NewWith(ctx context.Context, v Values) (*App, error)",
		// Copied out once, at the very top, into both the local and the
		// field — so `NewStore(flags, logger)` below reads no differently
		// from a call whose arguments were all constructed.
		"\tflags := v.Flags\n\ta.flags = flags\n",
		"NewStore(flags, logger)",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}

	// The App carries the supplied value as a field like any other node,
	// so App.Graph() and the Health/Ready walks see one uniform struct.
	// Matched loosely because gofmt aligns the struct's field types.
	if !regexp.MustCompile(`(?m)^\tflags +Flags$`).MatchString(src) {
		t.Errorf("the supplied value is not a field on App:\n%s", src)
	}
	// Nothing constructs it: the fixture's own NewFlags does not exist,
	// and no provider may be invented for a declared value.
	if strings.Contains(src, "flags :=") && !strings.Contains(src, "flags := v.Flags") {
		t.Errorf("the supplied value was built rather than taken from Values:\n%s", src)
	}
}

// TestEmitSuppliedValueKeepsAZeroValueNew pins the decision that the
// marker must not change the generated package's public API: New stays,
// with the signature it always had, delegating to NewWith with a zero
// Values. A graph that declared a value would otherwise lose New
// entirely, so adding one marker would break every existing call site.
func TestEmitSuppliedValueKeepsAZeroValueNew(t *testing.T) {
	resolved, spec := buildValuesResolved(t, "Store", "Flags")

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	if !strings.Contains(src, "func New(ctx context.Context) (*App, error) {\n\treturn NewWith(ctx, Values{})\n}") {
		t.Errorf("missing the zero-value New delegate:\n%s", src)
	}
	// The doc comment is the only warning a caller gets that the zero
	// value is a nil pointer for anything that is not a struct, so it is
	// part of the output, not decoration.
	if !strings.Contains(src, "// Prefer NewWith:") {
		t.Errorf("New's comment does not point at NewWith:\n%s", src)
	}
}

// TestEmitWithoutSuppliedValuesEmitsNeitherStructNorWithConstructor is the
// byte-identical claim from the other side: a graph that declares no
// servo.Value must emit exactly the file it emitted before the marker
// existed — one New, no Values type, and no `v` parameter to rename a
// node around.
func TestEmitWithoutSuppliedValuesEmitsNeitherStructNorWithConstructor(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)

	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	for _, unwanted := range []string{"type Values struct", "NewWith", "v Values"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("a graph with no servo.Value must not emit %q:\n%s", unwanted, src)
		}
	}
	if !strings.Contains(src, "func New(ctx context.Context) (*App, error)") {
		t.Errorf("New lost its historical signature:\n%s", src)
	}
}

// TestEmitTestModeNamesTheValuesStructTestValues extends the reason
// TestApp exists to the values struct. Both files land in one package, and
// an override can resolve a different set of values than production does,
// so a shared Values type would be two conflicting declarations of one
// name — and the override's own constructor has to be reachable under a
// name production does not already use.
func TestEmitTestModeNamesTheValuesStructTestValues(t *testing.T) {
	resolved, spec := buildValuesResolved(t, "Store", "Flags")

	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	wantSubstrings := []string{
		"type TestValues struct {\n\tFlags Flags\n}",
		"func NewTestAppWith(ctx context.Context, v TestValues) (*TestApp, error)",
		"func NewTestApp(ctx context.Context) (*TestApp, error) {\n\treturn NewTestAppWith(ctx, TestValues{})\n}",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(src, want) {
			t.Errorf("test-mode output missing %q\n---\n%s", want, src)
		}
	}
	// The production names would collide with the generated file sitting
	// beside this one in the same package.
	for _, unwanted := range []string{"type Values struct", "Values{}\n", "func NewWith("} {
		if strings.Contains(src, unwanted) {
			t.Errorf("test-mode output redeclares the production name %q:\n%s", unwanted, src)
		}
	}
}

// TestEmitTwoSuppliedValuesDoNotDereferenceAnAbsentProvider covers the
// shadow guard's one nil-safety case. Names are chosen for the whole set
// at once, supplied values first, and the guard asks of every *later*
// node which package identifiers constructing it will write. A supplied
// value is not constructed, so it has no provider function to read a
// package off — and it is only ever *later* than another node when two
// values are declared, which is why one value never reached this.
func TestEmitTwoSuppliedValuesDoNotDereferenceAnAbsentProvider(t *testing.T) {
	resolved, spec := buildValuesResolved(t, "Migrator", "Flags", "Target")

	// Emit panics with a nil-pointer dereference if the guard is dropped:
	// there is no Provider.Func to ask for a package.
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	// Both values are assigned, in declaration order, ahead of every
	// construction — Store needs Flags on the first line that follows.
	for _, want := range []string{"flags := v.Flags", "target := v.Target", "NewMigrator(store, target)"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}
	if strings.Index(src, "flags := v.Flags") > strings.Index(src, "NewStore(") {
		t.Errorf("a supplied value was assigned after the node that consumes it:\n%s", src)
	}
	// Neither value is qualified: nothing constructing either of them can
	// shadow a package, because nothing constructs them at all.
	for _, unwanted := range []string{"appFlags", "appTarget"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("a supplied value was package-qualified for a shadow it cannot cause (%s):\n%s", unwanted, src)
		}
	}
}
