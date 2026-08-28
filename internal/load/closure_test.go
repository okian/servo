package load

import (
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestImportClosureDeduplicatesRepeatedRoot covers walk's seen-guard: a
// package reachable from two different roots (here, the same root listed
// twice) must only be visited once, not walked into infinitely or
// duplicated in the result.
func TestImportClosureDeduplicatesRepeatedRoot(t *testing.T) {
	leaf := &packages.Package{PkgPath: "example.com/leaf"}
	root := &packages.Package{PkgPath: "example.com/root", Imports: map[string]*packages.Package{"example.com/leaf": leaf}}

	c := ImportClosure([]*packages.Package{root, root})
	if want := 2; len(c) != want {
		t.Fatalf("got %d packages in closure, want %d (root visited twice should still dedupe): %v", len(c), want, c)
	}
}

func TestPackagePathOfPredeclaredNamedHasNoPackage(t *testing.T) {
	errType := types.Universe.Lookup("error").Type()
	if _, ok := errType.(*types.Named); !ok {
		t.Fatalf("universe error is %T, want *types.Named (test assumption broken)", errType)
	}
	if _, ok := PackagePathOf(errType); ok {
		t.Errorf("PackagePathOf(error) ok = true, want false: predeclared types have no declaring package")
	}
}

func TestPackagePathOfUnnamedTypeReturnsFalse(t *testing.T) {
	if _, ok := PackagePathOf(types.Typ[types.Int]); ok {
		t.Errorf("PackagePathOf(int) ok = true, want false: a basic type is neither named nor a pointer")
	}
}
