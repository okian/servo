// Package servovet is the go/analysis analyzer for the servo mistakes the
// compiler cannot catch.
//
// The first is a marker call (Build/Root/Bind/Override/Scoped/Include/
// Value/ConfigFile/Linger/Max) in a file that doesn't carry a build
// constraint requiring the servoinject tag. Marker bodies panic when
// actually executed, so such a call would compile into the real binary and
// panic at runtime instead of being caught here, in the editor, before `go
// generate` ever runs.
//
// The second is a ScopeKey method with a receiver its body can reach.
// servo calls that method on a typed nil, and no signature can express
// "never dereferences the receiver" — so it is checked rather than
// assumed.
//
// The third is a broken //servo: comment directive — unrecognized name,
// malformed //servo:config options, or a directive placed where the
// generator never looks. A typo'd directive is otherwise just a comment:
// it compiles, generates, and silently does nothing.
//
// It lives in its own importable package, not in the command, so the
// integrations the reference promises are actually reachable:
// golangci-lint's module plugin system imports the analyzer package and
// registers Analyzer, which nothing can do with a var in package main.
// cmd/servo-vet is the singlechecker binary wrapping this.
package servovet

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// Analyzer is the servo analyzer, ready to hand to singlechecker,
// multichecker, unitchecker, golangci-lint's plugin registry, or
// analysistest.
var Analyzer = &analysis.Analyzer{
	Name: "servovet",
	Doc:  "flags servo marker calls in files missing the servoinject build tag, ScopeKey methods with a non-blank receiver, and malformed or misplaced //servo: directives",
	Run:  run,
}

var markerNames = map[string]bool{
	"Build": true, "Root": true, "Bind": true, "Override": true,
	"Scoped": true, "Linger": true, "Max": true,
	// Value, Include and ConfigFile panic when executed for the same
	// reason the rest do, and Include is the one most likely to be written
	// outside a spec file — a shared marker set lives in its own package,
	// where the tag is easy to forget.
	"Value": true, "Include": true, "ConfigFile": true,
}

func run(pass *analysis.Pass) (any, error) {
	checkScopeKeyReceivers(pass)
	checkConfigDirectives(pass)

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
