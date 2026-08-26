package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v2/internal/graph"
)

// Spec is the resolved contents of the single servo.Build(...) call: the
// injector's declared roots and explicit bindings. Root/Bind/Override are
// read as syntax — nothing here executes them.
type Spec struct {
	InjectorPkg *packages.Package
	File        *ast.File
	Pos         token.Position
	Roots       []RootDecl
	Binds       []BindDecl
	Overrides   []BindDecl
}

type RootDecl struct {
	Key  graph.Key
	Type types.Type
	Pos  token.Position
}

type BindDecl struct {
	Iface        graph.Key
	IfaceType    types.Type
	Concrete     graph.Key
	ConcreteType types.Type
	Pos          token.Position
}

// FindSpecs locates every servo.Build(...) call across the main module's
// packages and extracts each one's Root/Bind/Override arguments. Multiple
// specs in *different* packages is a normal multi-injector module — a
// monorepo with cmd/api, cmd/worker, cmd/migrator each wiring their own
// graph — and callers that can act on all of them (generate, check) should.
// Multiple specs in the *same* package is still an error: that package
// could only ever have one generated file, so two Build calls there are
// genuinely ambiguous, not a second injector.
func FindSpecs(l *Loaded) ([]*Spec, error) {
	var found []*Spec
	for _, pkg := range l.All {
		if pkg.Module == nil || !pkg.Module.Main {
			continue
		}
		for _, file := range pkg.Syntax {
			specs, err := specsInFile(pkg, file)
			if err != nil {
				return nil, err
			}
			found = append(found, specs...)
		}
	}
	if len(found) == 0 {
		return nil, errors.New("servo: no servo.Build(...) call found — run `servo init` to scaffold a spec file")
	}

	byPkg := map[string][]*Spec{}
	for _, s := range found {
		byPkg[s.InjectorPkg.PkgPath] = append(byPkg[s.InjectorPkg.PkgPath], s)
	}
	for pkgPath, specs := range byPkg {
		if len(specs) <= 1 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "servo: multiple servo.Build(...) calls found in the same package %s (ambiguous — which one owns the generated file?):\n", pkgPath)
		for _, s := range specs {
			fmt.Fprintf(&b, "  %s\n", s.Pos)
		}
		return nil, errors.New(b.String())
	}

	for _, s := range found {
		if err := checkBuildTag(s); err != nil {
			return nil, err
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].InjectorPkg.PkgPath < found[j].InjectorPkg.PkgPath })
	return found, nil
}

// FindSpec narrows FindSpecs to exactly one — for commands that inherently
// operate on a single injector (explain/why/list/graph/doctor) and need
// the caller to disambiguate with --dir when the scanned scope contains
// more than one.
func FindSpec(l *Loaded) (*Spec, error) {
	specs, err := FindSpecs(l)
	if err != nil {
		return nil, err
	}
	if len(specs) > 1 {
		var b strings.Builder
		b.WriteString("servo: multiple injectors found in this scope — pass --dir to pick one:\n")
		for _, s := range specs {
			fmt.Fprintf(&b, "  %s\n", s.Pos)
		}
		return nil, errors.New(b.String())
	}
	return specs[0], nil
}

// specsInFile finds every servo.Build(...) call in file, not just the
// first — two Build calls in the same file are exactly as ambiguous as two
// in the same package across different files, and FindSpecs' same-package
// check depends on seeing all of them rather than silently keeping only
// the last one found.
func specsInFile(pkg *packages.Package, file *ast.File) ([]*Spec, error) {
	var specs []*Spec
	var walkErr error
	ast.Inspect(file, func(n ast.Node) bool {
		if walkErr != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := resolveCalledFunc(pkg, call)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath || fn.Name() != "Build" {
			return true
		}
		s, err := parseBuildCall(pkg, file, call)
		if err != nil {
			walkErr = err
			return false
		}
		specs = append(specs, s)
		return false // no need to descend into this call's own arguments further
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return specs, nil
}

func resolveCalledFunc(pkg *packages.Package, call *ast.CallExpr) (*types.Func, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	fn, ok := pkg.TypesInfo.Uses[sel.Sel].(*types.Func)
	return fn, ok
}

func parseBuildCall(pkg *packages.Package, file *ast.File, call *ast.CallExpr) (*Spec, error) {
	spec := &Spec{InjectorPkg: pkg, File: file, Pos: pkg.Fset.Position(call.Pos())}

	for _, arg := range call.Args {
		argCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return nil, fmt.Errorf("%s: servo.Build argument is not a marker call", pkg.Fset.Position(arg.Pos()))
		}
		name, typeArgs, pos, err := markerCall(pkg, argCall)
		if err != nil {
			return nil, err
		}
		switch name {
		case "Root":
			if len(typeArgs) != 1 {
				return nil, fmt.Errorf("%s: servo.Root expects exactly one type argument", pos)
			}
			spec.Roots = append(spec.Roots, RootDecl{Key: graph.NewKey(typeArgs[0], ""), Type: typeArgs[0], Pos: pos})
		case "Bind", "Override":
			if len(typeArgs) != 2 {
				return nil, fmt.Errorf("%s: servo.%s expects exactly two type arguments", pos, name)
			}
			if isInterfaceType(typeArgs[1]) {
				return nil, fmt.Errorf("%s: servo.%s's second type argument must be a concrete type, not an interface (%s) — Bind/Override name the concrete implementation, they don't chain to another interface", pos, name, typeArgs[1].String())
			}
			decl := BindDecl{
				Iface: graph.NewKey(typeArgs[0], ""), IfaceType: typeArgs[0],
				Concrete: graph.NewKey(typeArgs[1], ""), ConcreteType: typeArgs[1],
				Pos: pos,
			}
			if name == "Bind" {
				if prior, dup := findByIface(spec.Binds, decl.Iface); dup {
					return nil, fmt.Errorf("%s: servo.Bind[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				spec.Binds = append(spec.Binds, decl)
			} else {
				if prior, dup := findByIface(spec.Overrides, decl.Iface); dup {
					return nil, fmt.Errorf("%s: servo.Override[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				spec.Overrides = append(spec.Overrides, decl)
			}
		default:
			return nil, fmt.Errorf("%s: unrecognized servo marker %q inside Build(...)", pos, name)
		}
	}
	return spec, nil
}

// findByIface returns the first declaration in decls bound to iface, so a
// second Bind (or, separately, a second Override) for the same interface
// can be reported against the position of the one already accepted,
// instead of silently letting the second one win with no diagnostic at
// all. Bind and Override are checked against their own list only:
// declaring both for the same interface is the documented, intentional way
// to get a servotest override, not a collision.
func findByIface(decls []BindDecl, iface graph.Key) (BindDecl, bool) {
	for _, d := range decls {
		if d.Iface == iface {
			return d, true
		}
	}
	return BindDecl{}, false
}

// isInterfaceType reports whether t is an interface (including the empty
// interface any) rather than a concrete type. Bind/Override's second type
// argument resolves via an exact-type lookup keyed on its own type string,
// which bypasses structural interface search entirely — binding it to
// another interface silently defeats that search rather than satisfying
// it, so it is rejected at declaration time instead of surfacing later as
// an unhelpful "no provider" diagnostic with no candidates listed.
func isInterfaceType(t types.Type) bool {
	_, ok := types.Unalias(t).Underlying().(*types.Interface)
	return ok
}

// markerCall extracts the marker function name and its explicit generic
// type arguments (via go/types instantiation info, never by executing
// anything) from a call like servo.Root[T]() or servo.Bind[I, C]().
func markerCall(pkg *packages.Package, call *ast.CallExpr) (string, []types.Type, token.Position, error) {
	pos := pkg.Fset.Position(call.Pos())

	var selIdent *ast.Ident
	switch fun := call.Fun.(type) {
	case *ast.IndexExpr:
		sel, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return "", nil, pos, fmt.Errorf("%s: unsupported marker call shape", pos)
		}
		selIdent = sel.Sel
	case *ast.IndexListExpr:
		sel, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return "", nil, pos, fmt.Errorf("%s: unsupported marker call shape", pos)
		}
		selIdent = sel.Sel
	default:
		return "", nil, pos, fmt.Errorf("%s: servo.Build argument must be a Root/Bind/Override call with explicit type arguments", pos)
	}

	fn, ok := pkg.TypesInfo.Uses[selIdent].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath {
		return "", nil, pos, fmt.Errorf("%s: not a servo marker call", pos)
	}

	inst, ok := pkg.TypesInfo.Instances[selIdent]
	if !ok {
		return "", nil, pos, fmt.Errorf("%s: servo.%s must be instantiated with explicit type arguments", pos, fn.Name())
	}

	typeArgs := make([]types.Type, inst.TypeArgs.Len())
	for i := range typeArgs {
		typeArgs[i] = inst.TypeArgs.At(i)
	}
	return fn.Name(), typeArgs, pos, nil
}

// checkBuildTag guards against an untagged spec file in the generator
// itself, not just in servo-vet: a spec file without a constraint that
// truly requires BuildTag would compile straight into the real binary.
func checkBuildTag(spec *Spec) error {
	if FileRequiresBuildTag(spec.File, BuildTag) {
		return nil
	}
	return fmt.Errorf("%s: spec file is missing a `//go:build %s` constraint — as written it would compile into the real binary", spec.Pos, BuildTag)
}

// FileRequiresBuildTag reports whether file carries a build constraint
// that can only be satisfied when tag is set. Exported so servo-vet can run
// the identical check without duplicating constraint-parsing logic.
func FileRequiresBuildTag(file *ast.File, tag string) bool {
	for _, group := range file.Comments {
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				continue
			}
			if requiresTag(expr, tag) {
				return true
			}
		}
	}
	return false
}

// requiresTag reports whether expr can only be true when tag is set, under
// the most permissive assumption about every other tag (each treated as
// true, the case most likely to let the constraint pass without tag).
func requiresTag(expr constraint.Expr, tag string) bool {
	couldPassWithoutTag := expr.Eval(func(t string) bool { return t != tag })
	return !couldPassWithoutTag
}
