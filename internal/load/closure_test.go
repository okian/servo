package load

import (
	"go/types"
	"testing"
)

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
