package resolve

import (
	"fmt"
	"go/types"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
)

// reach decides whether generated code in the injector package can refer
// to a provider at all: call the function, and name its result type as the
// field of the App that holds the node.
//
// Nothing checked this, and each way it can fail used to end the same way
// — `servo generate` wrote a file the compiler refused and called it a bug
// in servo:
//
//   - the provider, or a type its result names, lives under another tree's
//     internal/ directory, which Go forbids the injector to import;
//   - the result type is unexported — `func NewEvictor() *evictor` is fine
//     Go, but the App field holding it cannot be declared outside package
//     cache;
//   - the provider's package imports the injector's own package — an
//     adapter importing the core package it serves, with the spec file in
//     that core package — so importing it back is a cycle.
//
// Parameter types are deliberately not judged. An argument is passed as a
// value the generated code already holds, so its type is never written
// down: an interface parameter declared in an internal package is
// satisfied perfectly well by a node the injector can name. A //servo:config
// argument is the one exception — its loader is called in the injector —
// and is judged where it resolves, by configProblem.
type reach struct {
	injector string
	// importable is false for an injector in a package main, which nothing
	// can import and so nothing can close a cycle through.
	importable bool
	// importsInjector memoizes whether a package imports the injector
	// package, directly or through others.
	importsInjector map[*types.Package]bool
	problems        map[*graph.Provider]string
}

func newReach(injector string, importable bool) *reach {
	return &reach{injector: injector, importable: importable, importsInjector: map[*types.Package]bool{}, problems: map[*graph.Provider]string{}}
}

// providerProblem says why the injector cannot use p, as a clause that
// follows the provider's name — "its result type *cache.evictor is
// unexported, …" — or "" when it can. A Spec built by hand names no
// injector package, and then nothing is judged.
func (rc *reach) providerProblem(p *graph.Provider) string {
	if rc == nil || rc.injector == "" || p.Pkg == rc.injector {
		return ""
	}
	if why, ok := rc.problems[p]; ok {
		return why
	}
	why := ""
	if why = rc.packageProblem(p.Func.Pkg()); why != "" {
		why = fmt.Sprintf("it is declared in %s, %s", p.Pkg, why)
	} else {
		why = rc.resultProblem(p.ResultType)
	}
	rc.problems[p] = why
	return why
}

// configProblem says why the injector cannot call d's generated loader —
// New calls it from the injector package, so the config's own package has
// to be one the injector can import — or "".
func (rc *reach) configProblem(d *graph.ConfigDecl) string {
	if rc == nil || rc.injector == "" || d.PkgPath == rc.injector {
		return ""
	}
	named, ok := types.Unalias(d.Type).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return ""
	}
	if why := rc.packageProblem(named.Obj().Pkg()); why != "" {
		return fmt.Sprintf("its loader, %s.ServoConfig, is declared in %s, %s", d.PkgName, d.PkgPath, why)
	}
	return ""
}

// packageProblem says why the injector cannot import pkg, as a clause
// following the package's path, or "" when it can.
func (rc *reach) packageProblem(pkg *types.Package) string {
	if pkg == nil || pkg.Path() == rc.injector {
		return ""
	}
	path := pkg.Path()
	if parent, internal := internalParent(path); internal {
		if parent == "" {
			return "which only the standard library may import"
		}
		if !hasPathPrefix(rc.injector, parent) {
			return fmt.Sprintf("which %s cannot import: it is internal to %s — move one of the two so the injector sits inside that tree, or the type outside it", rc.injector, parent)
		}
	}
	if strings.HasPrefix(path, "vendor/") || strings.Contains(path, "/vendor/") {
		return "a vendored copy no other package may import"
	}
	if rc.imports(pkg) {
		return fmt.Sprintf("which imports %s, the injector's own package, so importing it back from there would be an import cycle — declare servo.Build in a package nothing imports, such as a cmd/ main", rc.injector)
	}
	return ""
}

// resultProblem finds a type in t the App cannot name — unexported in
// another package, or in a package the injector cannot import — and says
// which, or "".
func (rc *reach) resultProblem(t types.Type) string {
	hidden, why := rc.unnameable(t, map[types.Type]bool{})
	if hidden == nil {
		return ""
	}
	written := graph.TypeStringWith(t, packageNameQualifier)
	name := hidden.Pkg().Name() + "." + hidden.Name()
	whole := pointee(t) == types.Type(hidden.Type())
	switch {
	case why == "" && whole:
		return fmt.Sprintf("its result type %s is unexported, so the App generated in %s cannot name it — export the type, or return an exported interface it implements", written, rc.injector)
	case why == "":
		return fmt.Sprintf("its result type %s uses %s, which is unexported, so the App generated in %s cannot name it", written, name, rc.injector)
	default:
		return fmt.Sprintf("its result type %s is declared in %s, %s", written, hidden.Pkg().Path(), why)
	}
}

// unnameable walks t the way the emitter writes it — through pointers,
// type arguments and every composite — and returns the first named type
// the injector package cannot spell, with the package problem when that is
// the reason ("" when the type is merely unexported).
func (rc *reach) unnameable(t types.Type, seen map[types.Type]bool) (*types.TypeName, string) {
	if t == nil || seen[t] {
		return nil, ""
	}
	seen[t] = true
	walkTuple := func(tuple *types.Tuple) (*types.TypeName, string) {
		for i := range tuple.Len() {
			if obj, why := rc.unnameable(tuple.At(i).Type(), seen); obj != nil {
				return obj, why
			}
		}
		return nil, ""
	}
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		obj := u.Obj()
		if pkg := obj.Pkg(); pkg != nil && pkg.Path() != rc.injector {
			if !obj.Exported() {
				return obj, ""
			}
			if why := rc.packageProblem(pkg); why != "" {
				return obj, why
			}
		}
		for i := range u.TypeArgs().Len() {
			if obj, why := rc.unnameable(u.TypeArgs().At(i), seen); obj != nil {
				return obj, why
			}
		}
	case *types.Pointer:
		return rc.unnameable(u.Elem(), seen)
	case *types.Slice:
		return rc.unnameable(u.Elem(), seen)
	case *types.Array:
		return rc.unnameable(u.Elem(), seen)
	case *types.Chan:
		return rc.unnameable(u.Elem(), seen)
	case *types.Map:
		if obj, why := rc.unnameable(u.Key(), seen); obj != nil {
			return obj, why
		}
		return rc.unnameable(u.Elem(), seen)
	case *types.Signature:
		if obj, why := walkTuple(u.Params()); obj != nil {
			return obj, why
		}
		return walkTuple(u.Results())
	case *types.Struct:
		for i := range u.NumFields() {
			if obj, why := rc.unnameable(u.Field(i).Type(), seen); obj != nil {
				return obj, why
			}
		}
	case *types.Interface:
		for i := range u.NumExplicitMethods() {
			if obj, why := rc.unnameable(u.ExplicitMethod(i).Type(), seen); obj != nil {
				return obj, why
			}
		}
		for i := range u.NumEmbeddeds() {
			if obj, why := rc.unnameable(u.EmbeddedType(i), seen); obj != nil {
				return obj, why
			}
		}
	}
	return nil, ""
}

// imports reports whether pkg imports the injector package, directly or
// through any chain of imports. A package main is never imported, so an
// injector in one can never close a cycle.
func (rc *reach) imports(pkg *types.Package) bool {
	if pkg == nil || !rc.importable {
		return false
	}
	if got, ok := rc.importsInjector[pkg]; ok {
		return got
	}
	// Marked false while the walk is in progress, which ends any cycle
	// among the dependencies themselves.
	rc.importsInjector[pkg] = false
	found := false
	for _, imp := range pkg.Imports() {
		if imp.Path() == rc.injector || rc.imports(imp) {
			found = true
			break
		}
	}
	rc.importsInjector[pkg] = found
	return found
}

// internalParent applies Go's internal-path rule to an import path: the
// importer must sit in the tree rooted at the parent of the path's last
// "internal" element. It returns that parent ("" for the standard
// library's own internal/ packages), and whether the rule applies at all.
func internalParent(path string) (parent string, ok bool) {
	switch {
	case strings.HasSuffix(path, "/internal"):
		return strings.TrimSuffix(path, "/internal"), true
	case strings.Contains(path, "/internal/"):
		return path[:strings.LastIndex(path, "/internal/")], true
	case path == "internal" || strings.HasPrefix(path, "internal/"):
		return "", true
	}
	return "", false
}

// hasPathPrefix reports whether the slash-separated path s is prefix or
// lies under it, element-wise — example.com/a/b is under example.com/a,
// example.com/ab is not.
func hasPathPrefix(s, prefix string) bool {
	return s == prefix || strings.HasPrefix(s, prefix+"/")
}

// pointee is t's element if t is a pointer, else t, aliases resolved.
func pointee(t types.Type) types.Type {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		return types.Unalias(ptr.Elem())
	}
	return types.Unalias(t)
}

func packageNameQualifier(pkg *types.Package) string {
	return pkg.Name()
}
