// Command servo-vet is a go/analysis analyzer flagging servo marker calls
// (Build/Root/Bind/Override) in files that don't carry a build constraint
// requiring the servoinject tag. Marker bodies panic when actually executed,
// so a call left in an untagged file would compile into the real binary and
// panic at runtime instead of being caught here, in the editor, before
// `go generate` ever runs.
package main

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

var Analyzer = &analysis.Analyzer{
	Name: "servovet",
	Doc:  "flags servo.Build/Root/Bind/Override calls in files missing the servoinject build tag",
	Run:  run,
}

func main() {
	singlechecker.Main(Analyzer)
}

var markerNames = map[string]bool{"Build": true, "Root": true, "Bind": true, "Override": true}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		if load.FileRequiresBuildTag(file, load.BuildTag) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := markerFuncCalled(pass, call)
			if !ok {
				return true
			}
			pass.Reportf(call.Pos(),
				"servo: %s.%s called in a file without a `//go:build %s` constraint — it will compile into the real binary and panic at runtime; run `servo init` or add the tag",
				fn.Pkg().Name(), fn.Name(), load.BuildTag)
			return true
		})
	}
	return nil, nil
}

// markerFuncCalled reports the servo marker function call.Fun resolves to,
// handling both plain calls (Build) and generic instantiations (Root[T],
// Bind[I, C]).
func markerFuncCalled(pass *analysis.Pass, call *ast.CallExpr) (*types.Func, bool) {
	var ident *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		ident = fun.Sel
	case *ast.IndexExpr:
		sel, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return nil, false
		}
		ident = sel.Sel
	case *ast.IndexListExpr:
		sel, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return nil, false
		}
		ident = sel.Sel
	default:
		return nil, false
	}

	fn, ok := pass.TypesInfo.Uses[ident].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath || !markerNames[fn.Name()] {
		return nil, false
	}
	return fn, true
}
