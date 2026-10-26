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
		reject := func(reason string) {
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: reason, Result: wouldBeResult(sig)})
		}

		// A function declared in some other package main can never be
		// called from the injector: a main package is not importable, so a
		// generated file that named it would not compile. Left in the index
		// it would be selected — by exact type when the injector's own main
		// package shares a type with it, by structural search whenever a
		// sibling binary's main.go declares a type implementing an interface
		// this graph asks for — and `servo generate` would exit 0 on a file
		// `go build` then rejects.
		if pkg.Types.Name() == "main" && pkg.PkgPath != injectorPkgPath {
			reject("declared in another package main, which nothing can import")
			continue
		}
		if !fn.Exported() && pkg.PkgPath != injectorPkgPath {
			reject("unexported, outside injector package")
			continue
		}
		if sig.TypeParams().Len() > 0 {
			reject("generic function — unsupported")
			continue
		}
		if sig.Variadic() {
			// The variadic parameter's static type is a slice (e.g.
			// ...Option becomes []Option), and slices are never valid
			// provider results (ValidResultType), so it could never
			// resolve anyway — reject it here with a specific reason
			// instead of a confusing "no provider for []T" at resolve
			// time. Emission has no spread-call support for it either.
			reject("variadic parameter — unsupported")
			continue
		}

		resultType, hasCleanup, hasErr, reason := classifyResults(sig)
		if reason != "" {
			reject(reason)
			continue
		}
		if ok, reason := ValidResultType(resultType); !ok {
			reject(reason)
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
			recv := recvString(sig)
			qualifiedName := fmt.Sprintf("%s.%s.%s", pkg.Types.Name(), recv, m.Name())
			*rejected = append(*rejected, Rejected{Pkg: pkg.PkgPath, Name: qualifiedName, Pos: pos, Reason: "method, not a function", Result: wouldBeResult(sig), Recv: recv})
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
// via errorPositionReason. Every other mismatch is "does not match a
// supported result shape", followed by what is wrong with this one: the
// reason ends up beside the function's name in a "no provider" diagnostic,
// and "the error comes last" is something to act on where the bare phrase
// is not.
func classifyResults(sig *types.Signature) (result types.Type, hasCleanup, hasErr bool, reason string) {
	res := sig.Results()
	shape := func(detail string) (types.Type, bool, bool, string) {
		return nil, false, false, "does not match a supported result shape: " + detail
	}
	switch res.Len() {
	case 1:
		return res.At(0).Type(), false, false, ""
	case 2:
		first, second := res.At(0).Type(), res.At(1).Type()
		switch {
		case IsErrorType(second):
			return first, false, true, ""
		case isCleanupFunc(second):
			return first, true, false, ""
		case ImplementsError(second):
			return nil, false, false, errorPositionReason("second", second)
		case IsErrorType(first):
			return shape("the error comes last — return (T, error)")
		}
		return shape(fmt.Sprintf("the second result is %s, and must be error or a func() cleanup", second.String()))
	case 3:
		second, third := res.At(1).Type(), res.At(2).Type()
		switch {
		case isCleanupFunc(second) && IsErrorType(third):
			return res.At(0).Type(), true, true, ""
		case isCleanupFunc(second) && ImplementsError(third):
			return nil, false, false, errorPositionReason("third", third)
		case IsErrorType(second) && isCleanupFunc(third):
			return shape("the cleanup func comes before the error — return (T, func(), error)")
		}
		return shape("three results must be (T, func(), error)")
	}
	return shape(fmt.Sprintf("%d results — a provider returns one value, optionally followed by a func() cleanup and an error", res.Len()))
}

// wouldBeResult is the type a rejected function would provide were it
// accepted: its first result — or, for the (error, T) mistake, the T.
func wouldBeResult(sig *types.Signature) types.Type {
	res := sig.Results()
	if res.Len() == 2 && IsErrorType(res.At(0).Type()) && !IsErrorType(res.At(1).Type()) {
		return res.At(1).Type()
	}
	return res.At(0).Type()
}

// IsErrorType reports whether t is the predeclared error interface itself
// — not merely a type that satisfies it, which ImplementsError answers.
// Exported because the same question is asked of a handler's second result
// (internal/route), an accessor's third (internal/resolve), a traced
// method's last (internal/emit) and a ScopeKey's second (servovet), and one
// answer is better than five.
func IsErrorType(t types.Type) bool {
	return types.Identical(t, errorType)
}

// ImplementsError reports whether t satisfies the error interface
// structurally. Callers check IsErrorType first: a type that merely
// satisfies error is the specific mistake errorPositionReason names.
func ImplementsError(t types.Type) bool {
	return types.Implements(t, errorType.Underlying().(*types.Interface))
}

func errorPositionReason(position string, t types.Type) string {
	return fmt.Sprintf("%s result is %s, which implements error but is not the error interface itself — return error, not a type that merely satisfies it", position, t.String())
}

func isCleanupFunc(t types.Type) bool {
	sig, ok := t.Underlying().(*types.Signature)
	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 0 && !sig.Variadic()
}

// ValidResultType enforces that the primary result must be a named type,
// pointer-to-named, or a non-empty interface. Bare primitives, slices,
// arrays, maps, and the empty interface (any) are rejected outright —
// there is no opt-in mechanism to admit them. Exported because the same
// question, asked of a type nothing provides, is the reason a "no
// provider" diagnostic gives for it.
func ValidResultType(t types.Type) (ok bool, reason string) {
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
