package graph

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

func mustCheck(t *testing.T, importPath, filename, src string) *packages.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check(importPath, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck %s: %v", filename, err)
	}
	return &packages.Package{Name: pkg.Name(), PkgPath: importPath, Types: pkg, Fset: fset}
}

const storeSrc = `
package store

type DB struct{}
type Pool struct{}
type Cache struct{}
type Logger struct{}
type Store interface{ Get(key string) string }
type IntAlias = int

type MyError struct{}
func (e *MyError) Error() string { return "boom" }

type MyErrIface interface {
	error
	Code() int
}

func NewDB() *DB { return nil }
func NewPool() (*Pool, error) { return nil, nil }
func NewCache() (*Cache, func()) { return nil, nil }
func NewLogger() (*Logger, func(), error) { return nil, nil, nil }
func AsStore() Store { return nil }
func NewWithParam(p *Pool) *DB { return nil }

func NewID() string { return "" }
func NewAny() any { return nil }
func NewBadShape() (int, int) { return 0, 0 }
func NewGeneric[T any]() *Cache { return nil }
func NewBadPtr() *int { return nil }
func NewDoublePtr() **DB { return nil }
func NewSlice() []int { return nil }
func NewArray() [3]int { return [3]int{} }
func NewMap() map[string]int { return nil }
func NewChan() chan int { return nil }
func NewWithNamedError() (*DB, *MyError) { return nil, nil }
func NewWithCleanupAndNamedError() (*DB, func(), *MyError) { return nil, nil, nil }
func NewWithErrorIfaceResult() (*DB, MyErrIface) { return nil, nil }
func NewWithPointerToErrorIface() (*DB, *MyErrIface) { return nil, nil }

type Option func(*DB)
func NewVariadic(opts ...Option) *DB { return nil }

func newHidden() *DB { return nil }

func (p *Pool) Session() *DB { return nil }
func (p *Pool) session() *DB { return nil }
func (p *Pool) Close() {}
func (c Cache) Warm() *DB { return nil }
func Helper() {}
`

func findAccepted(t *testing.T, providers []*Provider, name string) *Provider {
	t.Helper()
	for _, p := range providers {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no accepted provider named %s (have %d providers)", name, len(providers))
	return nil
}

func findRejected(t *testing.T, rejected []Rejected, name string) Rejected {
	t.Helper()
	for _, r := range rejected {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no rejected entry named %s", name)
	return Rejected{}
}

func TestScanCandidatesAcceptedShapes(t *testing.T) {
	pkg := mustCheck(t, "example.com/store", "store.go", storeSrc)
	accepted, _ := ScanCandidates([]*packages.Package{pkg}, "example.com/store")

	cases := []struct {
		name       string
		hasCleanup bool
		hasErr     bool
		wantKey    string
	}{
		{"store.NewDB", false, false, "*example.com/store.DB"},
		{"store.NewPool", false, true, "*example.com/store.Pool"},
		{"store.NewCache", true, false, "*example.com/store.Cache"},
		{"store.NewLogger", true, true, "*example.com/store.Logger"},
		{"store.AsStore", false, false, "example.com/store.Store"},
		{"store.newHidden", false, false, "*example.com/store.DB"},
		{"store.NewWithParam", false, false, "*example.com/store.DB"},
	}
	for _, c := range cases {
		p := findAccepted(t, accepted, c.name)
		if p.HasCleanup != c.hasCleanup || p.HasError != c.hasErr {
			t.Errorf("%s: HasCleanup=%v HasError=%v, want %v/%v", c.name, p.HasCleanup, p.HasError, c.hasCleanup, c.hasErr)
		}
		if p.Result.String() != c.wantKey {
			t.Errorf("%s: Result=%s, want %s", c.name, p.Result.String(), c.wantKey)
		}
	}

	withParam := findAccepted(t, accepted, "store.NewWithParam")
	if len(withParam.Params) != 1 || withParam.Params[0].String() != "*example.com/store.Pool" {
		t.Errorf("NewWithParam.Params = %v, want a single *example.com/store.Pool", withParam.Params)
	}
	if len(withParam.ParamTypes) != 1 {
		t.Errorf("NewWithParam.ParamTypes = %v, want exactly one entry", withParam.ParamTypes)
	}
}

func TestScanCandidatesRejections(t *testing.T) {
	pkg := mustCheck(t, "example.com/store", "store.go", storeSrc)
	_, rejected := ScanCandidates([]*packages.Package{pkg}, "example.com/other")

	cases := []struct {
		name   string
		reason string
	}{
		{"store.NewID", "result type is a primitive (string)"},
		{"store.NewAny", "result type is any (empty interface)"},
		{"store.NewBadShape", "does not match a supported result shape"},
		{"store.NewGeneric", "generic function — unsupported"},
		{"store.NewVariadic", "variadic parameter — unsupported"},
		{"store.newHidden", "unexported, outside injector package"},
		{"store.(*Pool).Session", "method, not a function"},
		{"store.NewBadPtr", "result type is a pointer to an unnamed type (*int)"},
		{"store.NewDoublePtr", "result type is a pointer to an unnamed type (**example.com/store.DB)"},
		{"store.NewSlice", "result type is a slice"},
		{"store.NewArray", "result type is an array"},
		{"store.NewMap", "result type is a map"},
		{"store.NewChan", "result type is not a named type, pointer-to-named, or interface"},
		{"store.Cache.Warm", "method, not a function"},
		{"store.NewWithNamedError", "second result is *example.com/store.MyError, which implements error but is not the error interface itself — return error, not a type that merely satisfies it"},
		{"store.NewWithCleanupAndNamedError", "third result is *example.com/store.MyError, which implements error but is not the error interface itself — return error, not a type that merely satisfies it"},
		{"store.NewWithErrorIfaceResult", "second result is example.com/store.MyErrIface, which implements error but is not the error interface itself — return error, not a type that merely satisfies it"},
		{"store.NewWithPointerToErrorIface", "does not match a supported result shape"},
	}
	for _, c := range cases {
		r := findRejected(t, rejected, c.name)
		if r.Reason != c.reason {
			t.Errorf("%s: reason=%q, want %q", c.name, r.Reason, c.reason)
		}
	}

	// Helper() has zero results, (*Pool).session is unexported, and
	// (*Pool).Close() has zero results: none are candidates, so none are
	// rejected either — they must not appear in the list at all.
	for _, r := range rejected {
		switch r.Name {
		case "store.Helper":
			t.Errorf("Helper() has no results and must not appear in rejected list")
		case "store.(*Pool).session":
			t.Errorf("unexported method session() must not appear in rejected list")
		case "store.(*Pool).Close":
			t.Errorf("Close() has no results and must not appear in rejected list")
		}
	}
}

// TestScanCandidatesIgnoresTypeAliasAtPackageScope covers scanMethods'
// "not a *types.Named" branch: a package-scope type alias's TypeName.Type()
// resolves directly to the aliased type (here *types.Basic for int), so it
// must be skipped rather than treated as a type with methods.
func TestScanCandidatesIgnoresTypeAliasAtPackageScope(t *testing.T) {
	pkg := mustCheck(t, "example.com/store", "store.go", storeSrc)
	_, rejected := ScanCandidates([]*packages.Package{pkg}, "example.com/other")
	for _, r := range rejected {
		if r.Name == "store.IntAlias" {
			t.Errorf("a type alias must not be scanned as if it had methods")
		}
	}
}

func TestScanCandidatesUnexportedInsideInjectorPackage(t *testing.T) {
	pkg := mustCheck(t, "example.com/store", "store.go", storeSrc)
	accepted, rejected := ScanCandidates([]*packages.Package{pkg}, "example.com/store")

	findAccepted(t, accepted, "store.newHidden") // must not fatal

	for _, r := range rejected {
		if r.Name == "store.newHidden" {
			t.Fatalf("newHidden must be accepted (not rejected) when scanned as the injector's own package")
		}
	}
}

func TestScanCandidatesSortedAcrossPackages(t *testing.T) {
	pkgZ := mustCheck(t, "example.com/zzz", "aaa_first.go", `
package zzz
type Thing struct{}
func NewThing() *Thing { return nil }
`)
	pkgA := mustCheck(t, "example.com/store", "store.go", storeSrc)

	// Pass zzz second, but its file sorts alphabetically before store.go —
	// the result must be ordered by source position, not input order.
	accepted, _ := ScanCandidates([]*packages.Package{pkgA, pkgZ}, "example.com/store")
	if len(accepted) < 2 {
		t.Fatalf("expected at least 2 accepted providers, got %d", len(accepted))
	}
	if accepted[0].Name != "zzz.NewThing" {
		t.Errorf("first accepted provider = %s, want zzz.NewThing (aaa_first.go sorts before store.go)", accepted[0].Name)
	}
}
