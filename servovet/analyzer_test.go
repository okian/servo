package servovet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// pkgImporter shares object identity with a real go/packages load of the
// servo package, so the fixture's "github.com/okian/servo/v3/servo" import
// resolves to the exact same types.Package the analyzer's own
// pass.TypesInfo will reference. See internal/graph/capabilities_test.go
// for why this matters (a second, independent Check of "servo" would
// otherwise produce a non-identical package, breaking the fn.Pkg().Path()
// comparison here just as it broke types.Implements there).
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

// The servo package now imports net/http; loading it type-checks that whole
// tree, and every runOn call did it, so load it once for the binary. The
// result is read-only.
var (
	servoPkgOnce sync.Once
	servoPkgVal  *packages.Package
	servoPkgErr  string
)

func loadServoPackage(t *testing.T) *packages.Package {
	t.Helper()
	servoPkgOnce.Do(func() {
		cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
		pkgs, err := packages.Load(cfg, graph.ServoPackagePath)
		switch {
		case err != nil:
			servoPkgErr = err.Error()
		case len(pkgs) != 1 || pkgs[0].Types == nil:
			servoPkgErr = "expected exactly one loaded servo package"
		default:
			servoPkgVal = pkgs[0]
		}
	})
	if servoPkgErr != "" {
		t.Fatalf("load servo package: %s", servoPkgErr)
	}
	return servoPkgVal
}

// runOn type-checks src as one file and runs the analyzer directly against
// a hand-built analysis.Pass, returning every reported message.
func runOn(t *testing.T, src string) []string {
	t.Helper()
	servoPkg := loadServoPackage(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "spec.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Defs:      map[*ast.Ident]types.Object{},
		Uses:      map[*ast.Ident]types.Object{},
		Instances: map[*ast.Ident]types.Instance{},
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	pkg, err := conf.Check("example.com/fixture", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	var got []string
	pass := &analysis.Pass{
		Fset:      fset,
		Files:     []*ast.File{f},
		Pkg:       pkg,
		TypesInfo: info,
		Report:    func(d analysis.Diagnostic) { got = append(got, d.Message) },
	}
	if _, err := run(pass); err != nil {
		t.Fatalf("run: %v", err)
	}
	return got
}

func TestFlagsMarkerCallWithoutBuildTag(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[int](),
	)
}
`
	// Both the outer Build call and the nested Root call are flagged
	// independently: Go evaluates arguments before the call itself, so
	// servo.Root[int]() would panic even before servo.Build ever runs.
	got := runOn(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (Build and the nested Root): %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"servo.Build", "servo.Root"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics %v do not mention %q", got, want)
		}
	}
}

func TestDoesNotFlagCorrectlyTaggedFile(t *testing.T) {
	const src = `//go:build servoinject

package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[int](),
	)
}
`
	got := runOn(t, src)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics for a correctly tagged file, want 0: %v", len(got), got)
	}
}

func TestFlagsMultiTypeArgMarkerCall(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Bind[int, int](),
	)
}
`
	// As with the single-type-arg case, both the outer Build call and the
	// nested Bind[int, int]() (an IndexListExpr, not IndexExpr) are flagged.
	got := runOn(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (Build and the nested Bind): %v", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"servo.Build", "servo.Bind"} {
		if !strings.Contains(joined, want) {
			t.Errorf("diagnostics %v do not mention %q", got, want)
		}
	}
}

func TestDoesNotFlagUnqualifiedGenericCall(t *testing.T) {
	const src = `package fixture

func Ident[T any]() T { var zero T; return zero }

func wire() {
	_ = Ident[int]()
}
`
	got := runOn(t, src)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics for an unqualified (non-selector) generic call, want 0: %v", len(got), got)
	}
}

func TestDoesNotFlagUnqualifiedMultiArgGenericCall(t *testing.T) {
	const src = `package fixture

func Ident2[A, B any]() A { var zero A; return zero }

func wire() {
	_ = Ident2[int, string]()
}
`
	got := runOn(t, src)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics for an unqualified (non-selector) multi-arg generic call, want 0: %v", len(got), got)
	}
}

func TestDoesNotFlagPlainFunctionCall(t *testing.T) {
	const src = `package fixture

func helper() {}

func wire() {
	helper()
}
`
	got := runOn(t, src)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics for a plain (non-generic, non-selector) call, want 0: %v", len(got), got)
	}
}

func TestDoesNotFlagUnrelatedCalls(t *testing.T) {
	const src = `package fixture

import "fmt"

func wire() {
	fmt.Println("Build", "Root")
}
`
	got := runOn(t, src)
	if len(got) != 0 {
		t.Fatalf("got %d diagnostics for unrelated calls, want 0: %v", len(got), got)
	}
}

// TestMarkerDetectionCoversTheServoPackage: a marker is any servo function
// returning one of the opaque option types, and every such function panics
// when executed. The list of those types is the one thing kept by hand, so
// the expectation here is derived from the package itself — an exported
// function whose single result is a servo-declared empty struct — and the
// test fails the moment the package gains an option type the analyzer does
// not know.
func TestMarkerDetectionCoversTheServoPackage(t *testing.T) {
	servoPkg := loadServoPackage(t).Types
	scope := servoPkg.Scope()
	recognised := map[string]bool{}
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok || !fn.Exported() {
			continue
		}
		sig := fn.Type().(*types.Signature)
		want := name == "Build"
		if sig.Results().Len() == 1 {
			if named, ok := types.Unalias(sig.Results().At(0).Type()).(*types.Named); ok && named.Obj().Pkg() == servoPkg {
				if st, ok := named.Underlying().(*types.Struct); ok && st.NumFields() == 0 {
					want = true
				}
			}
		}
		if got := isMarkerFunc(fn); got != want {
			t.Errorf("isMarkerFunc(servo.%s) = %v, want %v", name, got, want)
		}
		recognised[name] = isMarkerFunc(fn)
	}
	for _, name := range []string{"Build", "Root", "Bind", "Override", "Scoped", "Linger", "Max", "Value", "Include", "ConfigFile", "HTTP", "Group", "Use", "Route", "Extract", "Trace", "Expected"} {
		if !recognised[name] {
			t.Errorf("servo.%s is not recognised as a marker", name)
		}
	}
	for _, name := range []string{"JSON", "XML", "Text", "Redirect", "WriteResponse", "RunStop", "MergeNodeResults", "LingerWindow"} {
		if recognised[name] {
			t.Errorf("servo.%s is a runtime function, not a marker", name)
		}
	}
}
