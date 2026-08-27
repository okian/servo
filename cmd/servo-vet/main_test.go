package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
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
