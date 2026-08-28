package main

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"

	"github.com/okian/servo/v3/internal/graph"
)

// checkScopeKeyReceivers flags a ScopeKey method whose body can reach its
// own receiver. Both an omitted name and a blank `_` are accepted; the
// omitted form is what this reports as the fix, because staticcheck's
// ST1006 flags `_` and asks for it.
//
// Generated code calls the method on a typed nil, because it needs the key
// before it can choose an instance — there is no instance to call it on.
// That is safe if and only if the method never touches the receiver, which
// the type system cannot express. `servo generate` rejects it too; this
// pass exists so the same mistake shows up as a squiggle in the editor
// rather than at the next `go generate`, and so it is caught in packages
// no injector has reached yet.
func checkScopeKeyReceivers(pass *analysis.Pass) {
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != graph.ScopeKeyMethodName || fn.Recv == nil {
				continue
			}
			if !scopeKeyShaped(pass, fn) || graph.ReceiverIsBlank(fn) {
				continue
			}
			pass.Reportf(fn.Recv.Pos(),
				"servo: %s must not name its receiver — servo calls it on a typed nil, so a receiver the body can reach is a nil dereference in production; write `func (*T) %s(...)`",
				graph.ScopeKeyMethodName, graph.ScopeKeyMethodName)
		}
	}
}

// scopeKeyShaped narrows the check to methods that really are key
// extractors: first parameter context.Context, results exactly (K, error)
// with K a defined non-error type. Anything else named ScopeKey is some
// other package's method and none of this analyzer's business.
func scopeKeyShaped(pass *analysis.Pass, fn *ast.FuncDecl) bool {
	obj, ok := pass.TypesInfo.Defs[fn.Name].(*types.Func)
	if !ok {
		return false // a package that did not type-check
	}
	// A *types.Func's type is always a signature, and the caller has
	// already established that this declaration has a receiver — so
	// neither is re-checked here.
	sig := obj.Type().(*types.Signature)
	if sig.Params().Len() == 0 || !graph.IsContextType(sig.Params().At(0).Type()) {
		return false
	}
	if sig.Results().Len() != 2 {
		return false
	}
	errType := types.Universe.Lookup("error").Type()
	if !types.Identical(sig.Results().At(1).Type(), errType) {
		return false
	}
	key, ok := types.Unalias(sig.Results().At(0).Type()).(*types.Named)
	if !ok {
		return false
	}
	_, isIface := key.Underlying().(*types.Interface)
	return !isIface
}
