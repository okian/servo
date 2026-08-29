package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// checkFixturePackage type-checks src as pkgPath, registers the result in
// imp so a package checked after it can import it, and wraps it the way
// graph.ScanCandidates expects. Every fixture in this file needs more than
// one package, because a node can only shadow a package identifier when
// the package it shadows is not the injector's own — inside the injector
// package emit writes bare, unqualified calls, and there is nothing to
// hide.
func checkFixturePackage(t *testing.T, imp *pkgImporter, fset *token.FileSet, pkgPath, src string) *packages.Package {
	t.Helper()
	f, err := parser.ParseFile(fset, path.Base(pkgPath)+".go", src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", pkgPath, err)
	}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check(pkgPath, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck %s: %v", pkgPath, err)
	}
	imp.byPath[pkgPath] = pkg
	return &packages.Package{Name: pkg.Name(), PkgPath: pkgPath, Types: pkg, Fset: fset}
}

// emitFixtureModule resolves root out of pkgs (the last of which is the
// injector) and returns the generated source.
func emitFixtureModule(t *testing.T, root types.Type, injector *packages.Package, pkgs ...*packages.Package) string {
	t.Helper()
	servoPkg := loadServoPackage(t)
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}
	candidates, _ := graph.ScanCandidates(pkgs, injector.PkgPath)
	scope := map[string]bool{}
	for _, p := range pkgs {
		scope[p.PkgPath] = true
	}

	spec := &load.Spec{
		InjectorPkg: injector,
		Roots:       []load.RootDecl{{Key: graph.NewKey(root, ""), Type: root, Pos: token.Position{Filename: "spec.go", Line: 5, Column: 3}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{Spec: spec, Candidates: candidates, Caps: caps, Scope: scope})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return string(out)
}

// injectorSrc is the package the generated file lands in. It declares
// nothing: every provider below lives elsewhere, which is what makes the
// calls package-qualified and the shadowing possible in the first place.
const injectorSrc = `
package app
`

const storePkgSrc = `
package store

type Store struct{}
func New() *Store { return &Store{} }

// Cache is constructed after Store, so store.NewCache is the call a local
// named store hides.
type Cache struct{}
func NewCache(s *Store) *Cache { return &Cache{} }
`

// TestEmitQualifiesANodeNamedAfterItsOwnPackage is the idiomatic half of
// the shadow guard: Store in package store takes the local `store`, and
// New declares every node in one flat scope, so every line after it
// resolves `store.NewCache` against that local instead of the package.
// The declaration itself is fine — the right-hand side is resolved before
// the variable exists — which is why a package providing exactly one node
// always worked and why this stayed latent until one provided two.
func TestEmitQualifiesANodeNamedAfterItsOwnPackage(t *testing.T) {
	fset := token.NewFileSet()
	imp := newPkgImporter(loadServoPackage(t))
	storePkg := checkFixturePackage(t, imp, fset, "example.com/shadow/store", storePkgSrc)
	injector := checkFixturePackage(t, imp, fset, "example.com/shadow/app", injectorSrc)

	cachePtr := types.NewPointer(storePkg.Types.Scope().Lookup("Cache").Type())
	src := emitFixtureModule(t, cachePtr, injector, storePkg, injector)

	if !strings.Contains(src, "storeStore := store.New()") {
		t.Errorf("the node was not qualified out of the way of its own package:\n%s", src)
	}
	if !strings.Contains(src, "cache := store.NewCache(storeStore)") {
		t.Errorf("the later call does not reach the package:\n%s", src)
	}
	// The exact line the compiler rejects.
	if strings.Contains(src, "store := store.New()") {
		t.Errorf("a local named store still hides the package it was built from:\n%s", src)
	}
}

const widgetPkgSrc = `
package widget

type Widget struct{}
`

const factoryPkgSrc = `
package factory

import "example.com/shadow/widget"

type Factory struct{}
func NewFactory() *Factory { return &Factory{} }

// NewWidget is qualified by factory at the call site while its result
// type is qualified by widget, so a node named factory shadows the caller
// even though no node is named after the widget package at all.
func NewWidget(f *Factory) *widget.Widget { return &widget.Widget{} }
`

// TestEmitQualifiesANodeShadowingALaterProvidersPackage covers the half
// the idiomatic case hides. Two packages are involved in constructing a
// node and they are not always the same one: the call is qualified by the
// provider function's package, the result type by its own. Comparing only
// result types let the guard pass for every constructor returning a type
// declared elsewhere — a factory or facade package, or the ordinary
// `func New(cfg Config) (*sql.DB, error)` shape.
func TestEmitQualifiesANodeShadowingALaterProvidersPackage(t *testing.T) {
	fset := token.NewFileSet()
	imp := newPkgImporter(loadServoPackage(t))
	widgetPkg := checkFixturePackage(t, imp, fset, "example.com/shadow/widget", widgetPkgSrc)
	factoryPkg := checkFixturePackage(t, imp, fset, "example.com/shadow/factory", factoryPkgSrc)
	injector := checkFixturePackage(t, imp, fset, "example.com/shadow/app", injectorSrc)

	widgetPtr := types.NewPointer(widgetPkg.Types.Scope().Lookup("Widget").Type())
	src := emitFixtureModule(t, widgetPtr, injector, widgetPkg, factoryPkg, injector)

	if !strings.Contains(src, "factoryFactory := factory.NewFactory()") {
		t.Errorf("the factory node was not qualified out of the way of the package that builds the widget:\n%s", src)
	}
	if !strings.Contains(src, "widget := factory.NewWidget(factoryFactory)") {
		t.Errorf("the widget is not constructed through the factory package:\n%s", src)
	}
	// The exact line the compiler rejects: `factory` is a *Factory here,
	// not the package, so factory.NewWidget does not resolve.
	if strings.Contains(src, "factory := factory.NewFactory()") {
		t.Errorf("a local named factory still hides the package NewWidget is called from:\n%s", src)
	}
	// The result type's own package is not what decided this, and nothing
	// is named after it: widget must keep its short name.
	if strings.Contains(src, "widgetWidget") {
		t.Errorf("the widget node was qualified for a collision it does not have:\n%s", src)
	}
}

// TestEmitLeavesTheLastNodeNamedAfterItsPackageAlone is the negative
// half, and the reason this bug stayed latent for so long: a node named
// after its own package is only a problem when a later line still has to
// reach that package. Declared last, `widget := widget.NewWidget(part)`
// is legal — the right-hand side resolves before the variable exists —
// and renaming it anyway would churn every generated file in the wild to
// no purpose.
func TestEmitLeavesTheLastNodeNamedAfterItsPackageAlone(t *testing.T) {
	const src = `
package widget

type Part struct{}
func NewPart() *Part { return &Part{} }

type Widget struct{}
func NewWidget(p *Part) *Widget { return &Widget{} }
`
	fset := token.NewFileSet()
	imp := newPkgImporter(loadServoPackage(t))
	widgetPkg := checkFixturePackage(t, imp, fset, "example.com/plain/widget", src)
	injector := checkFixturePackage(t, imp, fset, "example.com/plain/app", injectorSrc)

	widgetPtr := types.NewPointer(widgetPkg.Types.Scope().Lookup("Widget").Type())
	out := emitFixtureModule(t, widgetPtr, injector, widgetPkg, injector)

	if !strings.Contains(out, "part := widget.NewPart()") {
		t.Errorf("part was qualified even though nothing is named after its package:\n%s", out)
	}
	if !strings.Contains(out, "widget := widget.NewWidget(part)") {
		t.Errorf("the last node was renamed for a shadow no later line can suffer:\n%s", out)
	}
	if strings.Contains(out, "widgetWidget") {
		t.Errorf("the guard qualified a node it did not need to:\n%s", out)
	}
}

// TestQualifiesWithIgnoresAnEmptyIdentifier guards the one comparison
// that would misfire. packageNameOfType answers "" for a type with no
// package at all — a basic type, or an unnamed one — so an empty
// identifier reaching the guard would match it and report a shadow for a
// package that is never written anywhere.
func TestQualifiesWithIgnoresAnEmptyIdentifier(t *testing.T) {
	// A supplied value of a basic type: no package to name, no provider
	// to read one off either.
	n := &resolve.Node{Kind: resolve.NodeSupplied, SuppliedType: types.Typ[types.String]}

	if qualifiesWith(n, "") {
		t.Error("an empty identifier matched a type with no package: nothing in the generated file is qualified by it")
	}
	if qualifiesWith(n, "store") {
		t.Error("a type with no package was reported as shadowing store")
	}
}
