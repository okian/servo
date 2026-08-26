package load

import (
	"go/types"

	"golang.org/x/tools/go/packages"
)

// ImportClosure returns the import paths transitively reachable from start
// (inclusive). internal/resolve uses this to scope structural interface
// search to "packages transitively imported from the roots" rather than
// every package the candidate scan covers.
func ImportClosure(start []*packages.Package) map[string]bool {
	seen := make(map[string]bool)
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if seen[p.PkgPath] {
			return
		}
		seen[p.PkgPath] = true
		for _, dep := range p.Imports {
			walk(dep)
		}
	}
	for _, p := range start {
		walk(p)
	}
	return seen
}

// PackagePathOf returns the import path of the package declaring the named
// type underlying t (unwrapping one level of pointer), or false if t has no
// declaring package (e.g. a predeclared or anonymous type).
func PackagePathOf(t types.Type) (string, bool) {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		if u.Obj().Pkg() == nil {
			return "", false
		}
		return u.Obj().Pkg().Path(), true
	case *types.Pointer:
		return PackagePathOf(u.Elem())
	default:
		return "", false
	}
}
