package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
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
	// Positions renders positions the way the generated file commits them
	// — see Positions. Set by FindSpecs; nil on a Spec built by hand, which
	// leaves positions as they are.
	Positions *Positions
	File      *ast.File
	Pos       token.Position
	Roots     []RootDecl
	Binds     []BindDecl
	Overrides []BindDecl
	Scopes    []ScopeDecl
	Values    []ValueDecl
	// ConfigFile is the one servo.ConfigFile("...") declaration, or nil for
	// an env-only injector.
	ConfigFile *ConfigFileDecl

	// HTTP is non-nil when the Build call declares servo.HTTP(): this
	// injector serves the module's //servo: route directives on the
	// default group plus every group declared inside it. The routes
	// themselves come from a separate module-wide scan.
	HTTP *HTTPDecl

	// Extracts are the servo.Extract[T]() declarations: types whose
	// Extract method turns handler parameters of its result type into
	// per-request values.
	Extracts []ExtractDecl

	// Traces are the servo.Trace[I](...) declarations: interfaces whose
	// every consumer receives the generated OpenTelemetry wrapper in
	// place of the implementation I resolves to.
	Traces []TraceDecl

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

// HTTPDecl records a servo.HTTP(...) marker: the groups this injector
// serves beyond the implicit default, and the middleware attachments, in
// declaration order. Listener values still come from *servo.HTTPConfig at
// run time — only names and wiring live here.
type HTTPDecl struct {
	Pos    token.Position
	Groups []GroupDecl
	Uses   []UseDecl
}

// GroupDecl is one servo.Group("name") declaration inside servo.HTTP(...).
type GroupDecl struct {
	Name string
	Pos  token.Position
}

// UseDecl is one servo.Use[T](selectors...) attachment. Groups and Routes
// are mutually exclusive; both empty means server-level (every group).
type UseDecl struct {
	Type   graph.Key
	TypeT  types.Type
	Groups []string
	Routes []RouteSel
	Pos    token.Position
}

// RouteSel is one servo.Route("METHOD /pattern") selector inside a Use.
type RouteSel struct {
	Pattern string
	Pos     token.Position
}

// ExtractDecl is one servo.Extract[T]() declaration.
type ExtractDecl struct {
	Type  graph.Key
	TypeT types.Type
	Pos   token.Position
}

// TraceDecl is one servo.Trace[I](...) declaration. The interface is
// validated here, at parse time, because every rule about it is a rule
// about the type: it must be a named interface with at least one method,
// every method must be one the injector package can implement, and it
// must not be the OpenTelemetry API itself.
type TraceDecl struct {
	Iface     graph.Key
	IfaceType types.Type
	Pos       token.Position
	// Expected is every servo.Expected(pkg.ErrX) option, in declaration
	// order: errors the wrapper leaves off the span.
	Expected []ExpectedErr
	// Included marks a declaration spliced in by servo.Include; a local
	// one for the same interface supersedes it, exactly as a Bind does.
	Included bool
}

// ExpectedErr is one servo.Expected argument: the package-level variable
// or constant it names, read as syntax and never evaluated. The emitter
// renders it package-qualified inside the generated errors.Is check,
// which is why it has to be exported or declared in the injector package.
type ExpectedErr struct {
	Obj types.Object
	Pos token.Position
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
		s.Positions = l.Positions(s.InjectorPkg)
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
		case "HTTP":
			if spec.HTTP != nil {
				return fmt.Errorf("%s: servo.HTTP() declared twice — first at %s", pos, spec.HTTP.Pos)
			}
			decl, err := parseHTTPCall(pkg, argCall, pos)
			if err != nil {
				return err
			}
			spec.HTTP = decl
		case "Extract":
			if len(typeArgs) != 1 {
				return fmt.Errorf("%s: servo.Extract expects exactly one type argument", pos)
			}
			decl := ExtractDecl{Type: graph.NewKey(typeArgs[0], ""), TypeT: typeArgs[0], Pos: pos}
			for _, prior := range spec.Extracts {
				if prior.Type == decl.Type {
					return fmt.Errorf("%s: servo.Extract[%s] declared twice — first at %s", pos, decl.Type, prior.Pos)
				}
			}
			spec.Extracts = append(spec.Extracts, decl)
		case "Trace":
			decl, err := parseTraceCall(pkg, spec, argCall, typeArgs, pos)
			if err != nil {
				return err
			}
			// Same rule as Bind: a local declaration supersedes an
			// included one, so a shared marker set can trace an interface
			// and one injector can still add its own Expected list.
			if prior, dup := findTrace(spec.Traces, decl.Iface); dup {
				if prior.Included {
					replaceTrace(spec.Traces, decl)
					continue
				}
				return fmt.Errorf("%s: servo.Trace[%s] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
			}
			decl.Included = len(including) > 0
			spec.Traces = append(spec.Traces, decl)
		case "Expected":
			return fmt.Errorf("%s: servo.Expected belongs inside servo.Trace[I](...)", pos)
		case "Group", "Use":
			return fmt.Errorf("%s: servo.%s belongs inside servo.HTTP(...)", pos, name)
		case "Route":
			return fmt.Errorf("%s: servo.Route belongs inside servo.Use(...)", pos)
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

// findTrace is findByIface for Trace declarations.
func findTrace(decls []TraceDecl, iface graph.Key) (TraceDecl, bool) {
	for _, d := range decls {
		if d.Iface == iface {
			return d, true
		}
	}
	return TraceDecl{}, false
}

// replaceTrace is replaceByIface for Trace declarations.
func replaceTrace(decls []TraceDecl, decl TraceDecl) {
	for i, d := range decls {
		if d.Iface == decl.Iface {
			decls[i] = decl
			return
		}
	}
}

var errorIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// parseTraceCall reads one servo.Trace[I](options...) declaration. The
// interface is checked here for everything the emitter would otherwise
// discover while writing a type it cannot finish: a concrete type has no
// seam to wrap, an empty interface has nothing to wrap, an unexported
// method cannot be implemented from the injector package, and tracing the
// OpenTelemetry API itself would wrap the thing that creates the spans.
func parseTraceCall(pkg *packages.Package, spec *Spec, call *ast.CallExpr, typeArgs []types.Type, pos token.Position) (TraceDecl, error) {
	if len(typeArgs) != 1 {
		return TraceDecl{}, fmt.Errorf("%s: servo.Trace expects exactly one type argument", pos)
	}
	t := typeArgs[0]
	iface, ok := types.Unalias(t).Underlying().(*types.Interface)
	if !ok {
		return TraceDecl{}, fmt.Errorf("%s: servo.Trace's type argument must be an interface, not %s — tracing wraps the methods a consumer reaches through an interface, and a consumer holding a concrete type has no seam to wrap; make the consumer depend on the interface instead", pos, graph.TypeString(t))
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return TraceDecl{}, fmt.Errorf("%s: servo.Trace's type argument must be a named interface, not an interface literal — the span names and the wrapper's type name are derived from the interface's name", pos)
	}
	if iface.NumMethods() == 0 {
		return TraceDecl{}, fmt.Errorf("%s: servo.Trace[%s]: the interface declares no methods, so there is nothing to wrap", pos, graph.TypeString(t))
	}
	if named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == graph.OtelTracePackagePath {
		return TraceDecl{}, fmt.Errorf("%s: servo.Trace[%s]: the OpenTelemetry trace API is what every wrapper is built from, not something to wrap", pos, graph.TypeString(t))
	}
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if !m.Exported() && (m.Pkg() == nil || m.Pkg().Path() != spec.InjectorPkg.PkgPath) {
			return TraceDecl{}, fmt.Errorf("%s: servo.Trace[%s]: method %s is unexported, and the wrapper is generated into package %s, which cannot implement a method it cannot name — export the method, or declare the interface in the injector's own package", pos, graph.TypeString(t), m.Name(), spec.InjectorPkg.PkgPath)
		}
		// An exported method may still mention a type only its own package
		// can name — `Get(ctx, opts option)` — which consumers never notice
		// and the wrapper, which has to spell the signature out, cannot
		// compile. Said here, at the declaration, rather than by the
		// compiler inside a file the user is told not to read.
		if hidden := unexportedNamedIn(m.Type(), spec.InjectorPkg.PkgPath, map[types.Type]bool{}); hidden != nil {
			return TraceDecl{}, fmt.Errorf("%s: servo.Trace[%s]: method %s's signature uses %s, an unexported type the wrapper generated into package %s cannot name — export the type, or declare the interface in the injector's own package", pos, graph.TypeString(t), m.Name(), hiddenTypeString(hidden), spec.InjectorPkg.PkgPath)
		}
	}

	decl := TraceDecl{Iface: graph.NewKey(t, ""), IfaceType: t, Pos: pos}
	for _, arg := range call.Args {
		optCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return TraceDecl{}, fmt.Errorf("%s: servo.Trace option is not a marker call", pkg.Fset.Position(arg.Pos()))
		}
		name, _, optPos, err := markerCall(pkg, optCall)
		if err != nil {
			return TraceDecl{}, err
		}
		if name != "Expected" {
			return TraceDecl{}, fmt.Errorf("%s: servo.%s is not a servo.Trace option — the only option is servo.Expected(err)", optPos, name)
		}
		exp, err := parseExpectedCall(pkg, spec, optCall, optPos)
		if err != nil {
			return TraceDecl{}, err
		}
		for _, prior := range decl.Expected {
			if prior.Obj == exp.Obj {
				return TraceDecl{}, fmt.Errorf("%s: servo.Expected(%s) declared twice in the same servo.Trace — first at %s", optPos, exp.Obj.Name(), prior.Pos)
			}
		}
		decl.Expected = append(decl.Expected, exp)
	}
	return decl, nil
}

// hiddenTypeString names the type unexportedNamedIn found. An alias is
// named as the alias, not as what it stands for: graph.TypeString resolves
// aliases, and "uses store.Public, an unexported type" would be wrong
// about the one identifier the diagnostic is pointing at.
func hiddenTypeString(t types.Type) string {
	if a, ok := t.(*types.Alias); ok && a.Obj().Pkg() != nil {
		return a.Obj().Pkg().Path() + "." + a.Obj().Name()
	}
	return graph.TypeString(t)
}

// unexportedNamedIn finds a named or alias type inside t that only its own
// package can name from outside injectorPath: an unexported one declared
// anywhere else. It walks the composite types a signature can be built
// from, with seen guarding the recursion a self-referential type allows.
func unexportedNamedIn(t types.Type, injectorPath string, seen map[types.Type]bool) types.Type {
	if t == nil || seen[t] {
		return nil
	}
	seen[t] = true
	hidden := func(obj *types.TypeName) bool {
		return !obj.Exported() && obj.Pkg() != nil && obj.Pkg().Path() != injectorPath
	}
	switch u := t.(type) {
	case *types.Alias:
		if hidden(u.Obj()) {
			return t
		}
		return unexportedNamedIn(types.Unalias(t), injectorPath, seen)
	case *types.Named:
		if hidden(u.Obj()) {
			return t
		}
		if targs := u.TypeArgs(); targs != nil {
			for i := 0; i < targs.Len(); i++ {
				if h := unexportedNamedIn(targs.At(i), injectorPath, seen); h != nil {
					return h
				}
			}
		}
		return nil // a named type's underlying is reachable by its name
	case *types.Pointer:
		return unexportedNamedIn(u.Elem(), injectorPath, seen)
	case *types.Slice:
		return unexportedNamedIn(u.Elem(), injectorPath, seen)
	case *types.Array:
		return unexportedNamedIn(u.Elem(), injectorPath, seen)
	case *types.Chan:
		return unexportedNamedIn(u.Elem(), injectorPath, seen)
	case *types.Map:
		if h := unexportedNamedIn(u.Key(), injectorPath, seen); h != nil {
			return h
		}
		return unexportedNamedIn(u.Elem(), injectorPath, seen)
	case *types.Signature:
		for _, tuple := range []*types.Tuple{u.Params(), u.Results()} {
			for i := 0; i < tuple.Len(); i++ {
				if h := unexportedNamedIn(tuple.At(i).Type(), injectorPath, seen); h != nil {
					return h
				}
			}
		}
		return nil
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if h := unexportedNamedIn(u.Field(i).Type(), injectorPath, seen); h != nil {
				return h
			}
		}
		return nil
	case *types.Interface:
		for i := 0; i < u.NumMethods(); i++ {
			if h := unexportedNamedIn(u.Method(i).Type(), injectorPath, seen); h != nil {
				return h
			}
		}
		return nil
	default:
		return nil
	}
}

// parseExpectedCall reads servo.Expected's one argument, which must name
// a package-level variable or constant of an error type. It is read as
// syntax: the generated wrapper writes `errors.Is(err, pkg.ErrX)`, so the
// name is what servo needs, not the value — and the name has to be one
// the injector package can spell.
func parseExpectedCall(pkg *packages.Package, spec *Spec, call *ast.CallExpr, pos token.Position) (ExpectedErr, error) {
	const shape = "servo.Expected's argument must name a package-level error variable or constant, like cache.ErrMiss — it is read as syntax and never evaluated, so a call, a literal or a local has no value servo could read"
	if len(call.Args) != 1 {
		return ExpectedErr{}, fmt.Errorf("%s: servo.Expected takes exactly one argument — %s", pos, shape)
	}
	var ident *ast.Ident
	switch a := call.Args[0].(type) {
	case *ast.Ident:
		ident = a
	case *ast.SelectorExpr:
		ident = a.Sel
	default:
		return ExpectedErr{}, fmt.Errorf("%s: %s", pos, shape)
	}
	obj := pkg.TypesInfo.Uses[ident]
	switch obj.(type) {
	case *types.Var, *types.Const:
	default:
		return ExpectedErr{}, fmt.Errorf("%s: %s", pos, shape)
	}
	if obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return ExpectedErr{}, fmt.Errorf("%s: servo.Expected(%s): %s is not declared at package level — %s", pos, ident.Name, ident.Name, shape)
	}
	if !types.Implements(obj.Type(), errorIface) {
		return ExpectedErr{}, fmt.Errorf("%s: servo.Expected(%s): %s is a %s, not an error", pos, ident.Name, ident.Name, graph.TypeString(obj.Type()))
	}
	if !obj.Exported() && obj.Pkg().Path() != spec.InjectorPkg.PkgPath {
		return ExpectedErr{}, fmt.Errorf("%s: servo.Expected(%s): %s is unexported, and the generated errors.Is check lives in package %s, which cannot name it — export it", pos, ident.Name, ident.Name, spec.InjectorPkg.PkgPath)
	}
	return ExpectedErr{Obj: obj, Pos: pos}, nil
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

// groupNameRE is the grammar a group name must satisfy in both places it
// can appear: a servo.Group argument and a directive's trailing token.
var groupNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// parseHTTPCall parses servo.HTTP's option list: Group declarations and Use
// attachments, in declaration order. Everything here is read as syntax —
// which is why the string arguments must be constants.
func parseHTTPCall(pkg *packages.Package, call *ast.CallExpr, pos token.Position) (*HTTPDecl, error) {
	decl := &HTTPDecl{Pos: pos}
	for _, arg := range call.Args {
		optCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return nil, fmt.Errorf("%s: servo.HTTP option is not a marker call", pkg.Fset.Position(arg.Pos()))
		}
		name, typeArgs, optPos, err := markerCall(pkg, optCall)
		if err != nil {
			return nil, err
		}
		switch name {
		case "Group":
			gname, err := constStringArg(pkg, optCall, "servo.Group")
			if err != nil {
				return nil, err
			}
			if gname == "default" {
				return nil, fmt.Errorf(`%s: servo.Group("default") — "default" is the implicit group every servo.HTTP() injector serves; it needs no declaration`, optPos)
			}
			if !groupNameRE.MatchString(gname) {
				return nil, fmt.Errorf("%s: group name %q must match [A-Za-z0-9_-]+", optPos, gname)
			}
			for _, prior := range decl.Groups {
				if prior.Name == gname {
					return nil, fmt.Errorf("%s: servo.Group(%q) declared twice — first at %s", optPos, gname, prior.Pos)
				}
			}
			decl.Groups = append(decl.Groups, GroupDecl{Name: gname, Pos: optPos})
		case "Use":
			use, err := parseUseCall(pkg, optCall, typeArgs, optPos)
			if err != nil {
				return nil, err
			}
			decl.Uses = append(decl.Uses, use)
		case "Route":
			return nil, fmt.Errorf("%s: servo.Route belongs inside servo.Use(...)", optPos)
		default:
			return nil, fmt.Errorf("%s: servo.%s is not a servo.HTTP option — options are servo.Group and servo.Use", optPos, name)
		}
	}
	return decl, nil
}

// parseUseCall parses one servo.Use[T](selectors...) attachment. A Use
// selects groups or routes, never both: "wrap this group's mux" and "wrap
// this one route" compose differently, and a call meaning both reads as
// neither.
func parseUseCall(pkg *packages.Package, call *ast.CallExpr, typeArgs []types.Type, pos token.Position) (UseDecl, error) {
	if len(typeArgs) != 1 {
		return UseDecl{}, fmt.Errorf("%s: servo.Use expects exactly one type argument", pos)
	}
	use := UseDecl{Type: graph.NewKey(typeArgs[0], ""), TypeT: typeArgs[0], Pos: pos}
	for _, arg := range call.Args {
		selCall, ok := arg.(*ast.CallExpr)
		if !ok {
			return UseDecl{}, fmt.Errorf("%s: servo.Use selector is not a marker call", pkg.Fset.Position(arg.Pos()))
		}
		name, _, selPos, err := markerCall(pkg, selCall)
		if err != nil {
			return UseDecl{}, err
		}
		switch name {
		case "Group":
			gname, err := constStringArg(pkg, selCall, "servo.Group")
			if err != nil {
				return UseDecl{}, err
			}
			if !groupNameRE.MatchString(gname) {
				return UseDecl{}, fmt.Errorf("%s: group name %q must match [A-Za-z0-9_-]+", selPos, gname)
			}
			use.Groups = append(use.Groups, gname)
		case "Route":
			pattern, err := constStringArg(pkg, selCall, "servo.Route")
			if err != nil {
				return UseDecl{}, err
			}
			use.Routes = append(use.Routes, RouteSel{Pattern: pattern, Pos: selPos})
		default:
			return UseDecl{}, fmt.Errorf(`%s: servo.%s is not a servo.Use selector — use servo.Group("name") or servo.Route("METHOD /pattern")`, selPos, name)
		}
	}
	if len(use.Groups) > 0 && len(use.Routes) > 0 {
		return UseDecl{}, fmt.Errorf("%s: servo.Use[%s] selects groups or routes, not both — split it into two Use calls", pos, use.Type)
	}
	return use, nil
}

// constStringArg folds a marker's single string argument to its constant
// value — the spec is read as syntax, so a variable or call here has no
// value to read.
func constStringArg(pkg *packages.Package, call *ast.CallExpr, what string) (string, error) {
	pos := pkg.Fset.Position(call.Pos())
	if len(call.Args) != 1 {
		return "", fmt.Errorf("%s: %s expects exactly one argument", pos, what)
	}
	tv, ok := pkg.TypesInfo.Types[call.Args[0]]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", fmt.Errorf("%s: %s's argument must be a constant string", pos, what)
	}
	return constant.StringVal(tv.Value), nil
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
