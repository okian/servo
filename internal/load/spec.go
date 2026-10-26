package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/token"
	"go/types"
	"maps"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

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
	// Included marks a declaration spliced in by servo.Include; a local
	// one supersedes it, as a local Bind supersedes an included one.
	Included bool
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
	// Included marks a declaration spliced in by servo.Include.
	Included bool
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
	// Included marks a declaration spliced in by servo.Include.
	Included bool
}

// ConfigFileDecl is one servo.ConfigFile("path") — the config file this
// injector's //servo:config types read alongside the environment. The
// path's extension (validated here, at parse time) decides which decoder
// the generated code carries.
type ConfigFileDecl struct {
	Path string
	Pos  token.Position
	// Included marks a declaration spliced in by servo.Include.
	Included bool
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

// moduleSpecs memoizes ModuleSpecs: every caller asks the same question of
// the same load, so the specs are parsed once.
type moduleSpecs struct {
	once  sync.Once
	specs []*Spec
	err   error
}

// ModuleSpecs locates every servo.Build(...) call across the main module's
// packages (see Loaded.InMainModule) and extracts each one's declarations,
// whichever injectors Config.Dir selects — for the checks that are about the
// module rather than one graph, like a route group no injector declares,
// and as the ground FindSpecs selects from.
//
// Every spec in the module is read, and a malformed one fails the run
// wherever it is: the module-wide checks need all of them, and a run from
// an injector's own directory has to reach the verdict a run from the
// module root does.
//
// Multiple specs in *different* packages is a normal multi-injector module —
// a monorepo with cmd/api, cmd/worker, cmd/migrator each wiring their own
// graph. Multiple specs in the *same* package is still an error: that
// package could only ever have one generated file, so two Build calls there
// are genuinely ambiguous, not a second injector.
func ModuleSpecs(l *Loaded) ([]*Spec, error) {
	if l.specs == nil {
		return findModuleSpecs(l)
	}
	l.specs.once.Do(func() { l.specs.specs, l.specs.err = findModuleSpecs(l) })
	return slices.Clone(l.specs.specs), l.specs.err
}

func findModuleSpecs(l *Loaded) ([]*Spec, error) {
	var found []*Spec
	for _, pkg := range l.All {
		if !l.InMainModule(pkg) {
			continue
		}
		for _, file := range pkg.Syntax {
			specs, err := specsInFile(pkg, file)
			if err != nil {
				return nil, l.withVersions(err)
			}
			found = append(found, specs...)
		}
	}
	if len(found) == 0 {
		// A spec file whose Build call does not type-check — `servo.Buidl`,
		// or an import of the runtime that does not resolve — holds no call
		// servo can recognize, and "run `servo init`" is the wrong advice
		// for a file that is right there.
		for _, pkg := range l.All {
			if !l.InMainModule(pkg) {
				continue
			}
			if lines := specFileTypeErrors(pkg); len(lines) > 0 {
				return nil, fmt.Errorf("servo: no servo.Build(...) call could be read — a spec file does not type-check:\n\t%s", strings.Join(lines, "\n\t"))
			}
		}
		return nil, errors.New("servo: no servo.Build(...) call found — run `servo init` to scaffold a spec file")
	}

	byPkg := map[string][]*Spec{}
	for _, s := range found {
		byPkg[s.InjectorPkg.PkgPath] = append(byPkg[s.InjectorPkg.PkgPath], s)
	}
	for _, pkgPath := range slices.Sorted(maps.Keys(byPkg)) {
		specs := byPkg[pkgPath]
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

// FindSpecs is ModuleSpecs narrowed to the injectors Config.Dir selects —
// the ones generate, check and doctor process. A Dir that selects none is
// an error naming where the module's injectors are, since pointing --dir
// one level too deep — at a package that calls an injector rather than
// holds one — is the likely slip.
func FindSpecs(l *Loaded) ([]*Spec, error) {
	all, err := ModuleSpecs(l)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool)
	for _, p := range l.selectedPackages() {
		selected[p.PkgPath] = true
	}
	var specs []*Spec
	for _, s := range all {
		if selected[s.InjectorPkg.PkgPath] {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "servo: no servo.Build(...) call in %s or below — the module's injectors are elsewhere; point --dir at one of them, or at the module root for all of them:\n", l.dir)
		for _, s := range all {
			fmt.Fprintf(&b, "  %s\n", s.Pos)
		}
		return nil, errors.New(b.String())
	}
	return specs, nil
}

// FindSpec narrows FindSpecs to exactly one — for commands that inherently
// operate on a single injector (explain/why/list/graph/config) and need
// the caller to disambiguate with --dir when it selects more than one.
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
		if !ok || !isBuildCall(pkg, call) {
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

// isBuildCall reports whether call is a call of servo.Build itself,
// resolved through the type checker rather than by name.
func isBuildCall(pkg *packages.Package, call *ast.CallExpr) bool {
	fn, ok := resolveCalledFunc(pkg, call)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == graph.ServoPackagePath && fn.Name() == "Build"
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
		// Every declaration an Include can carry follows one rule: markers
		// are spliced in where the Include sits, so a declaration written
		// after it in the spec file finds the included one and replaces it
		// — the local file has the last word, which is the only ordering
		// that makes a shared set worth having. Two local declarations of
		// the same thing are still the duplicate they always were.
		included := len(including) > 0
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
			decl := ValueDecl{Key: graph.NewKey(typeArgs[0], ""), Type: typeArgs[0], Pos: pos, Included: included}
			if prior, dup := findValue(spec.Values, decl.Key); dup {
				if prior.Included {
					replaceValue(spec.Values, decl)
					continue
				}
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
			decl.Included = included
			if spec.ConfigFile != nil && !spec.ConfigFile.Included {
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
			if err := checkBindable(pkg, name, typeArgs[0], typeArgs[1], pos); err != nil {
				return err
			}
			decl := BindDecl{
				Iface: graph.NewKey(typeArgs[0], ""), IfaceType: typeArgs[0],
				Concrete: graph.NewKey(typeArgs[1], ""), ConcreteType: typeArgs[1],
				Pos: pos, Included: included,
			}
			if name == "Bind" {
				if prior, dup := findByIface(spec.Binds, decl.Iface); dup {
					if prior.Included {
						replaceByIface(spec.Binds, decl)
						continue
					}
					return fmt.Errorf("%s: servo.Bind[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				spec.Binds = append(spec.Binds, decl)
			} else {
				if prior, dup := findByIface(spec.Overrides, decl.Iface); dup {
					if prior.Included {
						replaceByIface(spec.Overrides, decl)
						continue
					}
					return fmt.Errorf("%s: servo.Override[%s, ...] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
				}
				spec.Overrides = append(spec.Overrides, decl)
			}
		case "Scoped":
			decl, err := parseScopedCall(pkg, argCall, typeArgs, pos)
			if err != nil {
				return err
			}
			decl.Included = included
			// A shared set can declare the scope and one injector can still
			// set its own Linger or Max. A duplicate that is not an
			// included-then-local pair is left for checkScopeDecls, which
			// names both positions.
			if prior, dup := findScoped(spec.Scopes, decl.Impl); dup && prior.Included {
				replaceScoped(spec.Scopes, decl)
				continue
			}
			spec.Scopes = append(spec.Scopes, decl)
		case "HTTP":
			if spec.HTTP != nil && !spec.HTTP.Included {
				return fmt.Errorf("%s: servo.HTTP() declared twice — first at %s", pos, spec.HTTP.Pos)
			}
			decl, err := parseHTTPCall(pkg, argCall, pos)
			if err != nil {
				return err
			}
			decl.Included = included
			spec.HTTP = decl
		case "Extract":
			if len(typeArgs) != 1 {
				return fmt.Errorf("%s: servo.Extract expects exactly one type argument", pos)
			}
			decl := ExtractDecl{Type: graph.NewKey(typeArgs[0], ""), TypeT: typeArgs[0], Pos: pos, Included: included}
			if prior, dup := findExtract(spec.Extracts, decl.Type); dup {
				if prior.Included {
					replaceExtract(spec.Extracts, decl)
					continue
				}
				return fmt.Errorf("%s: servo.Extract[%s] declared twice — first at %s", pos, decl.Type, prior.Pos)
			}
			spec.Extracts = append(spec.Extracts, decl)
		case "Trace":
			decl, err := parseTraceCall(pkg, spec, argCall, typeArgs, pos)
			if err != nil {
				return err
			}
			decl.Included = included
			if prior, dup := findTrace(spec.Traces, decl.Iface); dup {
				if prior.Included {
					replaceTrace(spec.Traces, decl)
					continue
				}
				return fmt.Errorf("%s: servo.Trace[%s] declared twice — first at %s", pos, decl.Iface.String(), prior.Pos)
			}
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
			if returnsServoType(calledMarker(pkg, argCall), "Marker") {
				return &markerSkewError{pos: pos, name: name, kind: "marker"}
			}
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

// replaceValue is replaceByIface for Value declarations.
func replaceValue(decls []ValueDecl, decl ValueDecl) {
	for i, d := range decls {
		if d.Key == decl.Key {
			decls[i] = decl
			return
		}
	}
}

// findExtract and replaceExtract are findByIface and replaceByIface for
// Extract declarations.
func findExtract(decls []ExtractDecl, t graph.Key) (ExtractDecl, bool) {
	for _, d := range decls {
		if d.Type == t {
			return d, true
		}
	}
	return ExtractDecl{}, false
}

func replaceExtract(decls []ExtractDecl, decl ExtractDecl) {
	for i, d := range decls {
		if d.Type == decl.Type {
			decls[i] = decl
			return
		}
	}
}

// findScoped and replaceScoped are the same pair for Scoped declarations,
// keyed by the scoped type.
func findScoped(decls []ScopeDecl, impl graph.Key) (ScopeDecl, bool) {
	for _, d := range decls {
		if d.Impl == impl {
			return d, true
		}
	}
	return ScopeDecl{}, false
}

func replaceScoped(decls []ScopeDecl, decl ScopeDecl) {
	for i, d := range decls {
		if d.Impl == decl.Impl {
			decls[i] = decl
			return
		}
	}
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
			if returnsServoType(calledMarker(pkg, optCall), "TraceOption") {
				return TraceDecl{}, &markerSkewError{pos: optPos, name: name, kind: "servo.Trace option"}
			}
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
	if !graph.ImplementsError(obj.Type()) {
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
var groupNameRE = regexp.MustCompile(graph.GroupNamePattern)

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
			if returnsServoType(calledMarker(pkg, optCall), "HTTPOption") {
				return nil, &markerSkewError{pos: optPos, name: name, kind: "servo.HTTP option"}
			}
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
			if returnsServoType(calledMarker(pkg, selCall), "HTTPOption") {
				return UseDecl{}, &markerSkewError{pos: selPos, name: name, kind: "servo.Use selector"}
			}
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
//
// Everything servo knows about a marker comes from the type checker, so a
// marker whose name or type argument does not resolve has no information
// to read at all. The type checker's own error for it — "undefined:
// api.Sever" — is the one that names the mistake, and since no build but
// servo's ever compiles a spec file, nothing else will say it; so that is
// what is reported, rather than a guess at what the missing information
// implies ("must be instantiated with explicit type arguments", for a
// mistyped type name).
func markerCall(pkg *packages.Package, call *ast.CallExpr) (string, []types.Type, token.Position, error) {
	pos := pkg.Fset.Position(call.Pos())

	var sel *ast.SelectorExpr
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		// A marker with no type parameters at all — servo.Linger/Max,
		// which are only legal inside a Scoped(...) argument list.
		// Resolved here rather than rejected outright so the caller can
		// say *that*, instead of the misleading "must be a Root/Bind/
		// Override/Scoped call with explicit type arguments".
		sel = fun
	case *ast.IndexExpr:
		s, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return "", nil, pos, fmt.Errorf("%s: unsupported marker call shape", pos)
		}
		sel = s
	case *ast.IndexListExpr:
		s, ok := fun.X.(*ast.SelectorExpr)
		if !ok {
			return "", nil, pos, fmt.Errorf("%s: unsupported marker call shape", pos)
		}
		sel = s
	default:
		return "", nil, pos, fmt.Errorf("%s: servo.Build argument must be a Root/Bind/Override/Scoped call with explicit type arguments", pos)
	}

	obj := pkg.TypesInfo.Uses[sel.Sel]
	if obj == nil {
		// `sevo.Root`, `servo.Rot`, or a marker the runtime this module
		// requires predates: the name itself did not resolve.
		if err := unresolvedMarker(pkg, sel, pos); err != nil {
			return "", nil, pos, err
		}
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath {
		return "", nil, pos, fmt.Errorf("%s: not a servo marker call", pos)
	}

	inst, hasInst := pkg.TypesInfo.Instances[sel.Sel]
	if !hasInst {
		if sig, ok := fn.Type().(*types.Signature); ok && sig.TypeParams().Len() > 0 {
			// A type argument that does not resolve leaves the marker
			// uninstantiated too, and is far likelier than a missing one.
			if err := specTypeError(pkg, pos, "servo."+fn.Name()+"'s type arguments"); err != nil {
				return "", nil, pos, err
			}
			return "", nil, pos, fmt.Errorf("%s: servo.%s must be instantiated with explicit type arguments", pos, fn.Name())
		}
		return fn.Name(), nil, pos, nil
	}

	typeArgs := make([]types.Type, inst.TypeArgs.Len())
	for i := range typeArgs {
		typeArgs[i] = inst.TypeArgs.At(i)
		if hasInvalid(typeArgs[i]) {
			if err := specTypeError(pkg, pos, "servo."+fn.Name()+"'s type arguments"); err != nil {
				return "", nil, pos, err
			}
		}
	}
	return fn.Name(), typeArgs, pos, nil
}

// calledMarker is the servo function call names, or nil.
func calledMarker(pkg *packages.Package, call *ast.CallExpr) *types.Func {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	fn, _ := pkg.TypesInfo.Uses[sel.Sel].(*types.Func)
	return fn
}

// returnsServoType reports whether fn is a function of the servo runtime
// package returning exactly its type name — Marker for a Build marker,
// HTTPOption, TraceOption or ScopeOption for an option. That is what makes
// a name this CLI does not recognize a marker of a newer runtime, rather
// than some other servo function written in the wrong place.
func returnsServoType(fn *types.Func, name string) bool {
	if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != graph.ServoPackagePath {
		return false
	}
	results := fn.Signature().Results()
	if results.Len() != 1 {
		return false
	}
	named, ok := types.Unalias(results.At(0).Type()).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == graph.ServoPackagePath && named.Obj().Name() == name
}

// knownMarkers is every servo function this CLI reads in a spec file — the
// names parseMarkerArgs and the option parsers switch on. A test keeps it
// equal to the runtime package's own marker functions, so a marker added
// to one cannot be missing from the other.
var knownMarkers = map[string]bool{
	"Build": true, "Root": true, "Bind": true, "Override": true, "Value": true,
	"Include": true, "ConfigFile": true, "Extract": true,
	"Scoped": true, "Linger": true, "Max": true,
	"HTTP": true, "Group": true, "Use": true, "Route": true,
	"Trace": true, "Expected": true,
}

// unresolvedMarker explains a servo.X whose X the type checker could not
// resolve. A name this CLI knows as a marker, on the servo runtime package
// itself, means the runtime the module requires is older than the CLI;
// anything else is the spec file's own type errors. Nil when there are
// none to report, leaving the caller its own diagnostic.
func unresolvedMarker(pkg *packages.Package, sel *ast.SelectorExpr, pos token.Position) error {
	if x, ok := sel.X.(*ast.Ident); ok && knownMarkers[sel.Sel.Name] {
		if pkgName, ok := pkg.TypesInfo.Uses[x].(*types.PkgName); ok && pkgName.Imported().Path() == graph.ServoPackagePath {
			return &markerSkewError{pos: pos, name: sel.Sel.Name, kind: "marker", cliKnows: true}
		}
	}
	return specTypeError(pkg, pos, "this marker")
}

// specTypeError is the type checker's own errors positioned inside pkg's
// spec files, headed by what servo could not read because of them — or nil
// when there are none. Only the spec files' errors are offered: the rest of
// the package is the build's to report, and an injector's main.go
// legitimately fails to compile before the first generation.
func specTypeError(pkg *packages.Package, pos token.Position, what string) error {
	lines := specFileTypeErrors(pkg)
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("%s: the spec file does not type-check, so %s cannot be read:\n\t%s", pos, what, strings.Join(lines, "\n\t"))
}

// specFileTypeErrors is "position: message" for every type error inside
// one of pkg's files gated on BuildTag — its spec files, and any shared
// marker set an Include names.
func specFileTypeErrors(pkg *packages.Package) []string {
	gated := map[string]bool{}
	for _, f := range pkg.Syntax {
		if FileRequiresBuildTag(f, BuildTag) {
			gated[pkg.Fset.Position(f.Package).Filename] = true
		}
	}
	if len(gated) == 0 {
		return nil
	}
	var lines []string
	for _, e := range pkg.TypeErrors {
		if p := e.Fset.Position(e.Pos); gated[p.Filename] {
			lines = append(lines, fmt.Sprintf("%s: %s", p, e.Msg))
		}
	}
	return lines
}

// hasInvalid reports whether t is, or is built from, the type the checker
// substitutes for one it could not resolve.
func hasInvalid(t types.Type) bool {
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		return u.Kind() == types.Invalid
	case *types.Pointer:
		return hasInvalid(u.Elem())
	case *types.Slice:
		return hasInvalid(u.Elem())
	case *types.Array:
		return hasInvalid(u.Elem())
	case *types.Map:
		return hasInvalid(u.Key()) || hasInvalid(u.Elem())
	case *types.Chan:
		return hasInvalid(u.Elem())
	case *types.Named:
		if targs := u.TypeArgs(); targs != nil {
			for i := 0; i < targs.Len(); i++ {
				if hasInvalid(targs.At(i)) {
					return true
				}
			}
		}
	}
	return false
}

// markerSkewError is a marker the CLI and the runtime package the module
// requires disagree about. The spec file is checked against the runtime
// the module's go.mod resolves, while the list of markers servo reads is
// the CLI's — so a CLI older than the runtime finds a marker it has never
// heard of, and a CLI newer than the runtime is handed a spec using one the
// runtime does not declare. Neither is a mistake in the spec, and both used
// to read as one ("unrecognized servo marker \"Trace\""), with nothing
// naming the two versions that disagree.
type markerSkewError struct {
	pos  token.Position
	name string // the marker, without the servo. qualifier
	kind string // "marker", "servo.HTTP option", …
	// cliKnows is set when this CLI knows the marker and the runtime does
	// not declare it; clear when the runtime declares a marker this CLI
	// does not know.
	cliKnows bool
	// cli and runtime are the two versions, "" where unknown — a
	// development build, a replaced runtime. FindSpecs fills them in.
	cli, runtime string
}

func (e *markerSkewError) Error() string {
	cli := "this servo CLI"
	if e.cli != "" {
		cli = "servo " + e.cli
	}
	runtime := graph.ServoPackagePath
	if e.runtime != "" {
		runtime += " " + e.runtime
	}
	if e.cliKnows {
		return fmt.Sprintf("%s: servo.%s is a %s %s knows, but %s, which the module requires, does not declare it — the module requires an older servo than the CLI generating it: update the module (`go get %s@latest`) to use servo.%s, or keep its release and generate with the CLI it pins (`go tool servo`)",
			e.pos, e.name, e.kind, cli, runtime, path.Dir(graph.ServoPackagePath), e.name)
	}
	return fmt.Sprintf("%s: servo.%s is a %s %s, which the module requires, declares and %s does not know — the module requires a newer servo than the CLI generating it: regenerate with the CLI the module pins (`go tool servo generate`), or update this one",
		e.pos, e.name, e.kind, runtime, cli)
}

// withVersions fills in the two versions a markerSkewError names; any
// other error passes through.
func (l *Loaded) withVersions(err error) error {
	var skew *markerSkewError
	if errors.As(err, &skew) {
		skew.cli, skew.runtime = l.cliVersion, l.RuntimeVersion()
	}
	return err
}

// checkBindable refuses a Bind or Override whose concrete type cannot stand
// where its interface is requested. Without it the declaration parsed, the
// graph resolved, and the generated file failed to compile — reported as a
// bug in servo, about a file the user is told not to read. The everyday
// cause is a mock generated before its interface last changed: `go
// generate ./...` runs servo's directive before the mock generator's when
// it comes first, so the mock is always one interface change behind at the
// moment servo reads it.
func checkBindable(pkg *packages.Package, marker string, iface, concrete types.Type, pos token.Position) error {
	if hasInvalid(iface) || hasInvalid(concrete) || types.AssignableTo(concrete, iface) {
		return nil
	}
	decl := fmt.Sprintf("servo.%s[%s, %s]()", marker, shortType(iface), shortType(concrete))
	it, ok := types.Unalias(iface).Underlying().(*types.Interface)
	if !ok {
		return fmt.Errorf("%s: %s: %s is not assignable to %s — %s names the implementation used wherever an interface is requested, so its first type argument is that interface", pos, decl, shortType(concrete), shortType(iface), marker)
	}
	msg := fmt.Sprintf("%s: %s: %s does not implement %s (%s)", pos, decl, shortType(concrete), shortType(iface), missingMethodReason(concrete, it, iface))
	if looksGenerated(pkg, concrete) {
		msg += fmt.Sprintf(" — if %s is a generated mock, it predates the interface's last change: regenerate it, then run servo generate again (go generate runs servo before a mock generator whose directive comes later)", shortType(concrete))
	}
	return errors.New(msg)
}

// missingMethodReason says why concrete does not implement iface the way
// the compiler would, naming the method: missing, a pointer receiver on a
// value type, or the wrong signature.
func missingMethodReason(concrete types.Type, it *types.Interface, iface types.Type) string {
	method, wrongType := types.MissingMethod(concrete, it, true)
	if method == nil {
		return "it does not satisfy the interface"
	}
	if _, isPtr := types.Unalias(concrete).(*types.Pointer); !isPtr && types.Implements(types.NewPointer(concrete), it) {
		return fmt.Sprintf("method %s has a pointer receiver, so only *%s does — bind that instead", method.Name(), shortType(concrete))
	}
	if wrongType {
		if have, ok := lookupMethod(concrete, method); ok {
			return fmt.Sprintf("wrong type for method %s: %s has %s, %s wants %s", method.Name(), shortType(concrete), shortType(have.Type()), shortType(iface), shortType(method.Type()))
		}
	}
	return "missing method " + method.Name()
}

// lookupMethod finds t's own method with want's name, pointer receivers
// included.
func lookupMethod(t types.Type, want *types.Func) (*types.Func, bool) {
	obj, _, _ := types.LookupFieldOrMethod(t, true, want.Pkg(), want.Name())
	fn, ok := obj.(*types.Func)
	return fn, ok
}

// shortType spells t the way the compiler's own messages do, packages by
// name: "*cache.Cache", not "*example.com/app/cache.Cache".
func shortType(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

// looksGenerated reports whether t's declaration reads like generated code
// — a mock above all: declared in a file with a generated-code header, in a
// package whose path says mock or fake, or named Mock…, Fake… or Stub….
func looksGenerated(pkg *packages.Package, t types.Type) bool {
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	obj := named.Obj()
	name := strings.ToLower(obj.Name())
	pkgPath := strings.ToLower(obj.Pkg().Path())
	if strings.HasPrefix(name, "mock") || strings.HasPrefix(name, "fake") || strings.HasPrefix(name, "stub") ||
		strings.Contains(pkgPath, "mock") || strings.Contains(pkgPath, "fake") {
		return true
	}
	for _, p := range allPackages(pkg) {
		if p.Types != obj.Pkg() {
			continue
		}
		for _, f := range p.Syntax {
			if f.Pos() <= obj.Pos() && obj.Pos() < f.End() {
				return ast.IsGenerated(f)
			}
		}
	}
	return false
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
