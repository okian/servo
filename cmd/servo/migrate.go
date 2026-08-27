package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type v1Registration struct {
	typeExpr string
	order    int
	pos      token.Position
}

// runMigrate reads v1 servo.Register(X{}, N) calls and emits a v3 skeleton
// plus a report flagging every duplicated order value. v1 has no
// constructor parameters at all — components find each other through
// globals — so there is no real dependency graph to derive a topological
// order from; this surfaces the old order values for human review rather
// than pretending to compute a correct one. Bodies are never rewritten:
// dependencies reached via globals need human judgement to become
// parameters.
func runMigrate(dir string) error {
	regs, err := findV1Registrations(dir)
	if err != nil {
		return err
	}
	if len(regs) == 0 {
		fmt.Println("servo migrate: no v1 Register(...) calls found under", dir)
		return nil
	}
	sort.SliceStable(regs, func(i, j int) bool { return regs[i].order < regs[j].order })

	fmt.Println("servo migrate report:")
	fmt.Println("  v1 has no constructor parameters, so there is no real dependency graph to derive")
	fmt.Println("  an order from — this only surfaces the OLD order values for review.")
	fmt.Println()

	countByOrder := map[int]int{}
	for _, r := range regs {
		countByOrder[r.order]++
	}
	for _, r := range regs {
		note := ""
		if countByOrder[r.order] > 1 {
			note = "  <- shares this order with another service: a likely latent ordering bug"
		}
		fmt.Printf("  order=%-4d %-30s %s%s\n", r.order, r.typeExpr, r.pos, note)
	}

	fmt.Println()
	fmt.Println("Skeleton spec (add real constructor dependencies by hand — v1's global-lookup")
	fmt.Println("style can't be inferred automatically):")
	fmt.Println()
	fmt.Println("//go:build servoinject")
	fmt.Println()
	fmt.Println("package main")
	fmt.Println()
	fmt.Println(`import "github.com/okian/servo/v3/servo"`)
	fmt.Println()
	fmt.Println("func wire() {")
	fmt.Println("\tservo.Build(")
	for _, r := range regs {
		fmt.Printf("\t\tservo.Root[*%s](), // was order=%d\n", r.typeExpr, r.order)
	}
	fmt.Println("\t)")
	fmt.Println("}")
	return nil
}

func findV1Registrations(dir string) ([]v1Registration, error) {
	var regs []v1Registration
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, ferr := parser.ParseFile(fset, path, nil, 0)
		if ferr != nil {
			return nil // skip unparseable files rather than aborting the whole scan
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 || calleeName(call.Fun) != "Register" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true
			}
			order, atoiErr := strconv.Atoi(lit.Value)
			if atoiErr != nil {
				return true
			}
			regs = append(regs, v1Registration{
				typeExpr: exprString(call.Args[0]),
				order:    order,
				pos:      fset.Position(call.Pos()),
			})
			return true
		})
		return nil
	})
	return regs, err
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	default:
		return ""
	}
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.CompositeLit:
		return exprString(x.Type)
	case *ast.UnaryExpr:
		return exprString(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	default:
		return "<expr>"
	}
}
