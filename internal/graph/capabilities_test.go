package graph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func loadServoPackage(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, ServoPackagePath)
	if err != nil {
		t.Fatalf("load servo package: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("expected exactly one loaded servo package, got %d", len(pkgs))
	}
	for _, e := range pkgs[0].Errors {
		t.Fatalf("servo package load error: %v", e)
	}
	return pkgs[0]
}

// pkgImporter resolves imports to the exact *types.Package objects a prior
// go/packages load produced, so a second, independent types.Config.Check
// (for a fixture that also imports "context") shares object identity with
// the first session. Without this, types.Implements structurally fails on
// every method with a context.Context parameter: the two sessions would
// otherwise produce two non-identical Named types both printing as
// "context.Context".
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
	return nil, fmt.Errorf("pkgImporter: package %q not found", path)
}

const capsFixtureSrc = `
package fixture

import "context"

type Full struct{}
func (*Full) Init(ctx context.Context) error   { return nil }
func (*Full) Run(ctx context.Context) error    { return nil }
func (*Full) Drain(ctx context.Context) error  { return nil }
func (*Full) Flush(ctx context.Context) error  { return nil }
func (*Full) Stop(ctx context.Context) error   { return nil }
func (*Full) Health(ctx context.Context) error { return nil }
func (*Full) Ready(ctx context.Context) error  { return nil }

type Plain struct{}

type OnlyRunner struct{}
func (OnlyRunner) Run(ctx context.Context) error { return nil }
`

func TestCapabilitiesDetect(t *testing.T) {
	servoPkg := loadServoPackage(t)
	caps, err := LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", capsFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	fixturePkg, err := conf.Check("example.com/fixture", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	typeNamed := func(name string) types.Type {
		obj := fixturePkg.Scope().Lookup(name)
		if obj == nil {
			t.Fatalf("no type %s in fixture", name)
		}
		return types.NewPointer(obj.Type())
	}

	full := caps.Detect(typeNamed("Full"))
	if len(full) != len(AllCapabilities) {
		t.Errorf("Full: got capabilities %v, want all of %v", full, AllCapabilities)
	}

	plain := caps.Detect(typeNamed("Plain"))
	if len(plain) != 0 {
		t.Errorf("Plain: got capabilities %v, want none", plain)
	}

	// OnlyRunner declares a value-receiver Run, so both *OnlyRunner and
	// OnlyRunner satisfy Runner.
	onlyRunnerObj := fixturePkg.Scope().Lookup("OnlyRunner").Type()
	got := caps.Detect(onlyRunnerObj)
	if len(got) != 1 || got[0] != "Runner" {
		t.Errorf("OnlyRunner: got %v, want [Runner]", got)
	}
}

func TestEmptyCapabilitiesDetectsNothing(t *testing.T) {
	servoPkg := loadServoPackage(t)
	caps := EmptyCapabilities()
	got := caps.Detect(types.NewPointer(servoPkg.Types.Scope().Lookup("Marker").Type()))
	if len(got) != 0 {
		t.Errorf("EmptyCapabilities: got %v, want none", got)
	}
}

func checkFixturePkg(t *testing.T, importPath, src string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: nil}
	pkg, err := conf.Check(importPath, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	return pkg
}

func TestLoadCapabilitiesMissingInterface(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/incomplete", `
package incomplete

type Initializer interface{ Init() }
`)
	_, err := LoadCapabilities(pkg)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got err=%v, want a 'not found' error for the missing capability interfaces", err)
	}
}

func TestLoadCapabilitiesNotAnInterface(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/wrongkind", `
package wrongkind

type Initializer struct{}
type Runner interface{}
type Drainer interface{}
type Flusher interface{}
type Finalizer interface{}
type Healther interface{}
type Readier interface{}
`)
	_, err := LoadCapabilities(pkg)
	if err == nil || !strings.Contains(err.Error(), "is not an interface") {
		t.Fatalf("got err=%v, want an 'is not an interface' error", err)
	}
}
