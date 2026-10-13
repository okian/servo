// Package servovet is the go/analysis analyzer for the servo mistakes the
// compiler cannot catch.
//
// The first is a marker call — servo.Build, or any servo function
// returning Marker, HTTPOption, ScopeOption or TraceOption — in a file that
// doesn't carry a build constraint requiring the servoinject tag. Marker bodies panic when
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

// markerResultTypes are the opaque types a marker function returns. A
// marker is recognised by its result rather than by name: every function in
// the servo package returning one of these panics when executed, so a new
// marker returning an existing option type is flagged with no change here,
// and nothing has to be kept in step with the parser in internal/load.
// Build, which returns nothing, is the one named exception.
// TestMarkerDetectionCoversTheServoPackage fails the moment the servo
// package gains an option type this map does not know.
var markerResultTypes = map[string]bool{"Marker": true, "HTTPOption": true, "ScopeOption": true, "TraceOption": true}

// isMarkerFunc reports whether fn is a servo marker: Build, or a function
// (not a method) of the servo package whose single result is one of
// markerResultTypes.
func isMarkerFunc(fn *types.Func) bool {
	if fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath {
		return false
	}
	if fn.Name() == "Build" {
		return true
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() != nil || sig.Results().Len() != 1 {
		return false
	}
	named, ok := types.Unalias(sig.Results().At(0).Type()).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == graph.ServoPackagePath && markerResultTypes[obj.Name()]
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
	if !ok || !isMarkerFunc(fn) {
		return nil, false
	}
	return fn, true
}
