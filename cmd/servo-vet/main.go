// Command servo-vet is a go/analysis analyzer for the two servo mistakes
// the compiler cannot catch.
//
// The first is a marker call (Build/Root/Bind/Override/Scoped/Linger/Max)
// in a file that doesn't carry a build constraint requiring the
// servoinject tag. Marker bodies panic when actually executed, so such a
// call would compile into the real binary and panic at runtime instead of
// being caught here, in the editor, before `go generate` ever runs.
//
// The second is a ScopeKey method with a receiver its body can reach.
// servo calls that method on a typed nil, and no signature can express
// "never dereferences the receiver" — so it is checked rather than
// assumed.
package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

var Analyzer = &analysis.Analyzer{
	Name: "servovet",
	Doc:  "flags servo marker calls in files missing the servoinject build tag, and ScopeKey methods with a non-blank receiver",
	Run:  run,
}

func main() {
	rejectInheritedTagsFlag(os.Args[1:])
	singlechecker.Main(Analyzer)
}

// rejectInheritedTagsFlag turns a silent lie into an error.
//
// go/analysis registers a -tags flag on every singlechecker binary and
// documents it as "no effect (deprecated)": checker.Run builds its own
// packages.Config with no BuildFlags, so `servo-vet -tags=prod ./...`
// exits 0 having analysed only the default configuration. Anyone who typed
// it believes prod was covered. There is no hook to make it work — the
// config is internal to x/tools — so the honest move is to refuse and name
// the invocation that does.
//
// Scanned from os.Args rather than registered as a flag: the flag already
// exists on flag.CommandLine by the time Main runs, and registering a
// second one panics.
func rejectInheritedTagsFlag(args []string) {
	for i, arg := range args {
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "-tags" && name != "--tags" {
			continue
		}
		if !hasValue && i+1 < len(args) {
			value = args[i+1]
		}
		if value == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, `servo-vet: -tags does not work here — it is go/analysis's own no-op flag, so this run would silently analyse only the default configuration.

To check a tagged configuration, drive servo-vet through the go command, which does understand build flags:

	go vet -tags=%s -vettool=$(which servo-vet) ./...
`, value)
		os.Exit(2)
	}
}

var markerNames = map[string]bool{
	"Build": true, "Root": true, "Bind": true, "Override": true,
	"Scoped": true, "Linger": true, "Max": true,
}

func run(pass *analysis.Pass) (any, error) {
	checkScopeKeyReceivers(pass)

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
