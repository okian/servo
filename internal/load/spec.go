package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
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
	Scopes      []ScopeDecl
	Values      []ValueDecl
	// ConfigFile is the one servo.ConfigFile("...") declaration, or nil for
	// an env-only injector.
	ConfigFile *ConfigFileDecl

	// Variant is the canonical tag set the load ran under, copied from
	// Loaded.Tags. Empty for a plain `servo generate`, which is what
	// keeps that case writing servo_gen.go exactly as it always has.
	Variant []string

	// GeneratedConstraint is the //go:build expression the emitted file
	// must carry so it compiles in the configuration this spec describes
	// and no other. Empty only for a Spec built by hand in a test, where
	// emit falls back to the historical `!servoinject`.
	GeneratedConstraint string
}

type RootDecl struct {
	Key  graph.Key
	Type types.Type
	Pos  token.Position
}

// ValueDecl is one servo.Value[T]() — a type the caller supplies to the
// generated NewWith rather than one any provider builds.
type ValueDecl struct {
	Key  graph.Key
	Type types.Type
	Pos  token.Position
}

// ConfigFileDecl is one servo.ConfigFile("path") — the config file this
// injector's //servo:config types read alongside the environment. The
// path's extension (validated here, at parse time) decides which decoder
// the generated code carries.
type ConfigFileDecl struct {
	Path string
	Pos  token.Position
}

type BindDecl struct {
	Iface        graph.Key
	IfaceType    types.Type
	Concrete     graph.Key
	ConcreteType types.Type
	Pos          token.Position
	// Included marks a declaration spliced in by servo.Include rather than
	// written in the spec file itself. A local declaration is allowed to
	// supersede an included one; two local ones for the same interface are
	// still the ambiguity they always were.
	Included bool
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
		s.Variant = l.Tags
		constraint, err := GeneratedConstraint(s.File, l.Tags)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Pos, err)
		}
		s.GeneratedConstraint = constraint
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
	if err := parseMarkerArgs(pkg, spec, call.Args, nil); err != nil {
		return nil, err
	}
	if err := checkScopeDecls(spec.Scopes); err != nil {
		return nil, err
	}
	return spec, nil
}

// parseMarkerArgs reads one marker argument list into spec. It is called
// for servo.Build's own arguments and, recursively, for the slice literal
// an Include names — the two are the same syntax, so they are read by the
// same code rather than by two that could drift.
//
// including is the chain of functions already being spliced, so a cycle is
// reported with the path that closed it instead of recursing forever.
func parseMarkerArgs(pkg *packages.Package, spec *Spec, args []ast.Expr, including []*types.Func) error {
	for _, arg := range args {
		argCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return fmt.Errorf("%s: servo.Build argument is not a marker call", pkg.Fset.Position(arg.Pos()))
		}
		name, typeArgs, pos, err := markerCall(pkg, argCall)
		if err != nil {
			return err
		}
		switch name {
		case "Root":
			if len(typeArgs) != 1 {
				return fmt.Errorf("%s: servo.Root expects exactly one type argument", pos)
			}
			spec.Roots = append(spec.Roots, RootDecl{Key: graph.NewKey(typeArgs[0], ""), Type: typeArgs[0], Pos: pos})
		case "Value":
			if len(typeArgs) != 1 {
				return fmt.Errorf("%s: servo.Value expects exactly one type argument", pos)
			}
			decl := ValueDecl{Key: graph.NewKey(typeArgs[0], ""), Type: typeArgs[0], Pos: pos}
			if prior, dup := findValue(spec.Values, decl.Key); dup {
				return fmt.Errorf("%s: servo.Value[%s]() declared twice — first at %s", pos, decl.Key.String(), prior.Pos)
			}
			spec.Values = append(spec.Values, decl)
		case "Include":
			if err := spliceInclude(pkg, spec, argCall, pos, including); err != nil {
				return err
			}
		case "ConfigFile":
			decl, err := parseConfigFileCall(argCall, pos)
			if err != nil {
				return err
			}
			if spec.ConfigFile != nil {
				return fmt.Errorf("%s: servo.ConfigFile(...) declared twice — first at %s", pos, spec.ConfigFile.Pos)
			}
			spec.ConfigFile = decl
		case "Bind", "Override":
			if len(typeArgs) != 2 {
				return fmt.Errorf("%s: servo.%s expects exactly two type arguments", pos, name)
			}
			if isInterfaceType(typeArgs[1]) {
				return fmt.Errorf("%s: servo.%s's second type argument must be a concrete type, not an interface (%s) — Bind/Override name the concrete implementation, they don't chain to another interface", pos, name, typeArgs[1].String())
			}
			decl := BindDecl{
				Iface: graph.NewKey(typeArgs[0], ""), IfaceType: typeArgs[0],
				Concrete: graph.NewKey(typeArgs[1], ""), ConcreteType: typeArgs[1],
				Pos: pos,
			}
			if name == "Bind" {
				// An Include's markers are spliced in where the Include
				// sits, so a Bind written after it in the spec file is a
				// duplicate of a shared one — and the local file wins,
				// which is the only ordering that makes a shared set worth
				// having. A duplicate between two *local* Binds is still
				// the ambiguity it always was.
				if prior, dup := findByIface(spec.Binds, decl.Iface); dup {
					if prior.Included {
						replaceByIface(spec.Binds, decl)
						continue
					}
					return fmt.Errorf("%s: servo.Bind[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				decl.Included = len(including) > 0
				spec.Binds = append(spec.Binds, decl)
			} else {
				if prior, dup := findByIface(spec.Overrides, decl.Iface); dup {
					if prior.Included {
						replaceByIface(spec.Overrides, decl)
						continue
					}
					return fmt.Errorf("%s: servo.Override[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				decl.Included = len(including) > 0
				spec.Overrides = append(spec.Overrides, decl)
			}
		case "Scoped":
			decl, err := parseScopedCall(pkg, argCall, typeArgs, pos)
			if err != nil {
				return err
			}
			spec.Scopes = append(spec.Scopes, decl)
		case "Linger", "Max":
			return fmt.Errorf("%s: servo.%s is a scope option, not a Build marker — it belongs inside a servo.Scoped[T, I](...) argument list", pos, name)
		default:
			return fmt.Errorf("%s: unrecognized servo marker %q inside Build(...)", pos, name)
		}
	}
	return nil
}

// parseConfigFileCall reads servo.ConfigFile's one argument, which must be
// a string literal — the path is read as syntax exactly as every marker
// argument is, and the extension is checked here so a typo'd one fails at
// the declaration instead of surfacing as a confusing decoder error from
// generated code.
func parseConfigFileCall(call *ast.CallExpr, pos token.Position) (*ConfigFileDecl, error) {
	if len(call.Args) != 1 {
		return nil, fmt.Errorf("%s: servo.ConfigFile takes exactly one argument, a string literal path", pos)
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, fmt.Errorf("%s: servo.ConfigFile's argument must be a string literal — it is read as syntax, so a variable or a call is a value servo would have to execute the program to know", pos)
	}
	path, err := strconv.Unquote(lit.Value)
	if err != nil || path == "" {
		return nil, fmt.Errorf("%s: servo.ConfigFile's argument is not a usable path", pos)
	}
	switch filepath.Ext(path) {
	case ".json", ".yaml", ".yml", ".toml":
		return &ConfigFileDecl{Path: path, Pos: pos}, nil
	default:
		return nil, fmt.Errorf("%s: servo.ConfigFile(%q): the extension must be .json, .yaml, .yml, or .toml — it decides which decoder the generated code carries", pos, path)
	}
}

// findValue is findByIface for Value declarations.
func findValue(decls []ValueDecl, k graph.Key) (ValueDecl, bool) {
	for _, d := range decls {
		if d.Key == k {
			return d, true
		}
	}
	return ValueDecl{}, false
}

// replaceByIface overwrites the declaration bound to decl.Iface in place,
// so a local declaration supersedes an included one without changing where
// in the list it sits.
func replaceByIface(decls []BindDecl, decl BindDecl) {
	for i, d := range decls {
		if d.Iface == decl.Iface {
			decls[i] = decl
			return
		}
	}
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
	case *ast.SelectorExpr:
		// A marker with no type parameters at all — servo.Linger/Max,
		// which are only legal inside a Scoped(...) argument list.
		// Resolved here rather than rejected outright so the caller can
		// say *that*, instead of the misleading "must be a Root/Bind/
		// Override/Scoped call with explicit type arguments".
		selIdent = fun.Sel
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
		return "", nil, pos, fmt.Errorf("%s: servo.Build argument must be a Root/Bind/Override/Scoped call with explicit type arguments", pos)
	}

	fn, ok := pkg.TypesInfo.Uses[selIdent].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath {
		return "", nil, pos, fmt.Errorf("%s: not a servo marker call", pos)
	}

	inst, hasInst := pkg.TypesInfo.Instances[selIdent]
	if !hasInst {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.TypeParams().Len() > 0 {
			return "", nil, pos, fmt.Errorf("%s: servo.%s must be instantiated with explicit type arguments", pos, fn.Name())
		}
		return fn.Name(), nil, pos, nil
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
// the identical check without duplicating constraint-parsing logic —
// literally identical: both go through FileConstraint, so the analyzer in
// your editor and the generator can never disagree about whether a file is
// gated.
func FileRequiresBuildTag(file *ast.File, tag string) bool {
	expr, ok := FileConstraint(file)
	return ok && requiresTag(expr, tag)
}

// requiresTag reports whether expr can only be true when tag is set, under
// the most permissive assumption about every other tag (each treated as
// true, the case most likely to let the constraint pass without tag).
func requiresTag(expr constraint.Expr, tag string) bool {
	couldPassWithoutTag := expr.Eval(func(t string) bool { return t != tag })
	return !couldPassWithoutTag
}

// spliceInclude reads the marker list returned by the function a
// servo.Include names and appends it to spec, in place.
//
// The function is never called. Its body is read as syntax, exactly as
// Build's own argument list is — which is why the shape it must have is
// narrow and checked: one return statement, of one slice literal of marker
// calls. Anything else (a variable, a conditional, an append) would be a
// program servo would have to run to know the answer to, and the whole
// point of the spec file is that it is read and never run.
func spliceInclude(pkg *packages.Package, spec *Spec, call *ast.CallExpr, pos token.Position, including []*types.Func) error {
	if len(call.Args) != 1 {
		return fmt.Errorf("%s: servo.Include takes exactly one argument, the name of a func() []servo.Marker", pos)
	}
	fn, ok := includedFunc(pkg, call.Args[0])
	if !ok {
		return fmt.Errorf("%s: servo.Include's argument must name a declared func() []servo.Marker — not a literal, a method value, or a call", pos)
	}
	for _, seen := range including {
		if seen == fn {
			var b strings.Builder
			fmt.Fprintf(&b, "%s: servo.Include cycle — %s includes itself, through:\n", pos, fn.Name())
			for _, f := range including {
				fmt.Fprintf(&b, "  %s\n", f.FullName())
			}
			return errors.New(b.String())
		}
	}

	decl, declPkg, ok := findFuncDecl(pkg, fn)
	if !ok {
		return fmt.Errorf("%s: servo.Include names %s, whose declaration is not in this build — a shared marker set must carry the `//go:build %s` constraint, the same as a spec file", pos, fn.FullName(), BuildTag)
	}
	if !FileRequiresBuildTag(fileOf(declPkg, decl), BuildTag) {
		return fmt.Errorf("%s: servo.Include names %s, which is declared in a file without a `//go:build %s` constraint — as written it would compile into the real binary, where every marker it returns panics", pos, fn.FullName(), BuildTag)
	}
	elts, err := markerSliceLiteral(declPkg, decl)
	if err != nil {
		return err
	}
	return parseMarkerArgs(declPkg, spec, elts, append(append([]*types.Func{}, including...), fn))
}

// includedFunc resolves the identifier or selector servo.Include was given
// to the function it names.
func includedFunc(pkg *packages.Package, arg ast.Expr) (*types.Func, bool) {
	var ident *ast.Ident
	switch a := arg.(type) {
	case *ast.Ident:
		ident = a
	case *ast.SelectorExpr:
		ident = a.Sel
	default:
		return nil, false
	}
	fn, ok := pkg.TypesInfo.Uses[ident].(*types.Func)
	if !ok || fn.Signature().Recv() != nil {
		return nil, false
	}
	return fn, true
}

// findFuncDecl locates fn's declaration and the package whose TypesInfo
// resolves the marker calls inside it. The declaring package is not
// necessarily the injector's: a shared marker set living in its own
// package is the whole reason Include exists.
func findFuncDecl(from *packages.Package, fn *types.Func) (*ast.FuncDecl, *packages.Package, bool) {
	for _, p := range allPackages(from) {
		if p.Types == nil || p.Types != fn.Pkg() {
			continue
		}
		for _, file := range p.Syntax {
			for _, d := range file.Decls {
				decl, ok := d.(*ast.FuncDecl)
				if !ok || decl.Recv != nil || decl.Name == nil {
					continue
				}
				if p.TypesInfo.Defs[decl.Name] == fn {
					return decl, p, true
				}
			}
		}
	}
	return nil, nil, false
}

// allPackages walks from's import graph, which is where a package other
// than the injector's own is reachable from. Visiting is breadth-first and
// deduplicated by path, so a diamond is walked once.
func allPackages(from *packages.Package) []*packages.Package {
	seen := map[string]bool{from.PkgPath: true}
	queue := []*packages.Package{from}
	var out []*packages.Package
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		out = append(out, p)
		paths := make([]string, 0, len(p.Imports))
		for path := range p.Imports {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if seen[path] {
				continue
			}
			seen[path] = true
			queue = append(queue, p.Imports[path])
		}
	}
	return out
}

func fileOf(pkg *packages.Package, decl *ast.FuncDecl) *ast.File {
	for _, f := range pkg.Syntax {
		if f.Pos() <= decl.Pos() && decl.Pos() <= f.End() {
			return f
		}
	}
	return &ast.File{}
}

// markerSliceLiteral extracts the elements of the one slice literal decl
// returns.
func markerSliceLiteral(pkg *packages.Package, decl *ast.FuncDecl) ([]ast.Expr, error) {
	pos := pkg.Fset.Position(decl.Pos())
	shape := fmt.Errorf("%s: %s must be exactly `return []servo.Marker{ ...marker calls... }` — its body is read as syntax and never run, so anything servo would have to execute to know the answer is refused", pos, decl.Name.Name)
	if decl.Body == nil || len(decl.Body.List) != 1 {
		return nil, shape
	}
	ret, ok := decl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil, shape
	}
	lit, ok := ret.Results[0].(*ast.CompositeLit)
	if !ok {
		return nil, shape
	}
	return lit.Elts, nil
}
