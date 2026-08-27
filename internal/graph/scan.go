package graph

import (
	"fmt"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

var errorType = types.Universe.Lookup("error").Type()

// ScanCandidates walks every loaded package's top-level scope and classifies
// each function with at least one result as an accepted provider or a
// rejected candidate with a reason. Functions with zero results are not
// attempting to construct anything, so they are neither accepted nor
// rejected — including them would flood `servo list --rejected` with every
// ordinary helper function in the module.
//
// injectorPkgPath is the package containing the servo.Build(...) spec file;
// unexported functions are candidates only there.
func ScanCandidates(pkgs []*packages.Package, injectorPkgPath string) (accepted []*Provider, rejected []Rejected) {
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scanFuncs(pkg, injectorPkgPath, &accepted, &rejected)
		scanMethods(pkg, &rejected)
	}

	sort.Slice(accepted, func(i, j int) bool { return ComparePos(accepted[i].Pos, accepted[j].Pos) < 0 })
	sort.Slice(rejected, func(i, j int) bool { return ComparePos(rejected[i].Pos, rejected[j].Pos) < 0 })
	return accepted, rejected
}

func scanFuncs(pkg *packages.Package, injectorPkgPath string, accepted *[]*Provider, rejected *[]Rejected) {
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok {
			continue
		}
		sig := fn.Type().(*types.Signature)
		if sig.Results().Len() == 0 {
			continue
		}

		pos := pkg.Fset.Position(fn.Pos())
		qualifiedName := pkg.Types.Name() + "." + name

		if !fn.Exported() && pkg.PkgPath != injectorPkgPath {
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: "unexported, outside injector package"})
			continue
		}
		if sig.TypeParams().Len() > 0 {
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: "generic function — unsupported"})
			continue
		}
		if sig.Variadic() {
			// The variadic parameter's static type is a slice (e.g.
			// ...Option becomes []Option), and slices are never valid
			// provider results (validResultType), so it could never
			// resolve anyway — reject it here with a specific reason
			// instead of a confusing "no provider for []T" at resolve
			// time. Emission has no spread-call support for it either.
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: "variadic parameter — unsupported"})
			continue
		}

		resultType, hasCleanup, hasErr, reason := classifyResults(sig)
		if reason != "" {
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: reason})
			continue
		}
		if ok, reason := validResultType(resultType); !ok {
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: reason})
			continue
		}

		paramTypes := paramTypeList(sig)
		paramKeyList := make([]Key, len(paramTypes))
		for i, pt := range paramTypes {
			paramKeyList[i] = NewKey(pt, "")
		}
		*accepted = append(*accepted, &Provider{
			Result:     NewKey(resultType, ""),
			ResultType: resultType,
			Params:     paramKeyList,
			ParamTypes: paramTypes,
			Func:       fn,
			Pkg:        pkg.PkgPath,
			Name:       qualifiedName,
			Pos:        pos,
			HasCleanup: hasCleanup,
			HasError:   hasErr,
			Unexported: !fn.Exported(),
		})
	}
}

// scanMethods records every exported method as rejected ("method, not a
// function"). Methods are never scope-level objects, so without this pass
// they would go entirely unreported rather than explained.
func scanMethods(pkg *packages.Package, rejected *[]Rejected) {
	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		for i := 0; i < named.NumMethods(); i++ {
			m := named.Method(i)
			if !m.Exported() {
				continue
			}
			sig := m.Type().(*types.Signature)
			if sig.Results().Len() == 0 {
				continue
			}
			pos := pkg.Fset.Position(m.Pos())
			qualifiedName := fmt.Sprintf("%s.%s.%s", pkg.Types.Name(), recvString(sig), m.Name())
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: "method, not a function"})
		}
	}
}

func recvString(sig *types.Signature) string {
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok {
			return "(*" + named.Obj().Name() + ")"
		}
	}
	if named, ok := recv.(*types.Named); ok {
		return named.Obj().Name()
	}
	return "?"
}

// classifyResults matches sig's results against the four accepted shapes:
// T; (T, error); (T, func()); (T, func(), error). The second (or third)
// result must be the error interface itself, not merely a type that
// satisfies it — a concrete error type gets a specific rejection reason
// via errorPositionReason rather than falling into the generic
// "does not match a supported result shape".
func classifyResults(sig *types.Signature) (result types.Type, hasCleanup, hasErr bool, reason string) {
	res := sig.Results()
	switch res.Len() {
	case 1:
		return res.At(0).Type(), false, false, ""
	case 2:
		second := res.At(1).Type()
		switch {
		case isErrorType(second):
			return res.At(0).Type(), false, true, ""
		case isCleanupFunc(second):
			return res.At(0).Type(), true, false, ""
		case implementsError(second):
			return nil, false, false, errorPositionReason("second", second)
		}
	case 3:
		second, third := res.At(1).Type(), res.At(2).Type()
		if isCleanupFunc(second) {
			if isErrorType(third) {
				return res.At(0).Type(), true, true, ""
			}
			if implementsError(third) {
				return nil, false, false, errorPositionReason("third", third)
			}
		}
	}
	return nil, false, false, "does not match a supported result shape"
}

func isErrorType(t types.Type) bool {
	return types.Identical(t, errorType)
}

// implementsError reports whether t satisfies the error interface
// structurally without being the error interface itself — the case
// isErrorType already rejected before this is checked.
func implementsError(t types.Type) bool {
	return types.Implements(t, errorType.Underlying().(*types.Interface))
}

func errorPositionReason(position string, t types.Type) string {
	return fmt.Sprintf("%s result is %s, which implements error but is not the error interface itself — return error, not a type that merely satisfies it", position, t.String())
}

func isCleanupFunc(t types.Type) bool {
	sig, ok := t.Underlying().(*types.Signature)
	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 0 && !sig.Variadic()
}

// validResultType enforces that the primary result must be a named type,
// pointer-to-named, or a non-empty interface. Bare primitives, slices,
// arrays, maps, and the empty interface (any) are rejected outright —
// there is no opt-in mechanism to admit them.
func validResultType(t types.Type) (ok bool, reason string) {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return true, ""
	case *types.Pointer:
		if _, ok := types.Unalias(u.Elem()).(*types.Named); ok {
			return true, ""
		}
		return false, fmt.Sprintf("result type is a pointer to an unnamed type (%s)", u.String())
	case *types.Interface:
		if u.NumMethods() == 0 {
			return false, "result type is any (empty interface)"
		}
		return true, ""
	case *types.Basic:
		return false, fmt.Sprintf("result type is a primitive (%s)", u.String())
	case *types.Slice:
		return false, "result type is a slice"
	case *types.Array:
		return false, "result type is an array"
	case *types.Map:
		return false, "result type is a map"
	default:
		return false, "result type is not a named type, pointer-to-named, or interface"
	}
}

func paramTypeList(sig *types.Signature) []types.Type {
	tuple := sig.Params()
	result := make([]types.Type, tuple.Len())
	for i := 0; i < tuple.Len(); i++ {
		result[i] = tuple.At(i).Type()
	}
	return result
}
