// Package load wraps golang.org/x/tools/go/packages to load the main
// module and every transitively imported package in one type-checking
// session (so identity-based checks like types.Implements are valid across
// package boundaries), and to locate + parse the servo.Build(...) spec
// call.
package load

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// BuildTag is the build constraint that gates the spec file. Loading always
// activates it, so `servo generate` sees the spec file (read as syntax,
// never executed) and never the previously generated output for the same
// injector.
const BuildTag = "servoinject"

// Config controls where and what to load.
type Config struct {
	// Dir is the working directory to load from; "" means the current
	// directory.
	Dir string
	// Pattern is the go/packages pattern to load; "" defaults to "./...".
	Pattern string
	// Build are the go command build flags passed through to
	// go/packages, which decide which files and packages the load can
	// see at all. The zero value loads the default configuration.
	Build BuildFlags
}

// Loaded is every package reachable from Pattern, deduplicated and sharing
// one type-checking session.
type Loaded struct {
	// Tags is the canonical user tag set this load ran under (BuildTag
	// excluded, since it is always present). It identifies the variant:
	// the generated file's name and its build constraint both derive
	// from it.
	Tags     []string
	Fset     *token.FileSet
	All      []*packages.Package // every loaded package, deduplicated — the candidate scan's universe
	ByPath   map[string]*packages.Package
	Roots    []*packages.Package // packages matching the input pattern
	ServoPkg *packages.Package
}

func Load(cfg Config) (*Loaded, error) {
	if err := cfg.Build.Validate(); err != nil {
		return nil, err
	}
	pattern := cfg.Pattern
	if pattern == "" {
		pattern = "./..."
	}
	pcfg := &packages.Config{
		Dir: cfg.Dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		BuildFlags: cfg.Build.Args(),
	}
	if cfg.Build.Overlay != "" {
		overlay, err := readOverlay(cfg.Build.Overlay)
		if err != nil {
			return nil, err
		}
		pcfg.Overlay = overlay
	}
	roots, err := packages.Load(pcfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	// Package errors are not fatal here: on a fresh checkout, before the
	// first `servo generate` has ever run, the injector's own package
	// legitimately has an "undefined: New" (or similar) error, since main.go
	// references the not-yet-generated output. go/types still populates
	// Uses/Instances for everything that DOES type-check (the
	// servo.Build(...) call itself does not depend on New/App at all), so
	// FindSpec below works fine regardless. NonInjectorErrors is the
	// narrower, correct check: real problems in packages other than the
	// injector are still fatal.

	all := make([]*packages.Package, 0, len(roots))
	byPath := make(map[string]*packages.Package)
	var fset *token.FileSet
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if _, ok := byPath[p.PkgPath]; ok {
			return
		}
		byPath[p.PkgPath] = p
		all = append(all, p)
		if fset == nil {
			fset = p.Fset
		}
		for _, dep := range p.Imports {
			walk(dep)
		}
	}
	for _, r := range roots {
		walk(r)
	}

	servoPkg := byPath[graph.ServoPackagePath]
	if servoPkg == nil {
		// Blaming the spec file's imports is right when there is no spec
		// file, and actively misleading once build tags are in play: the
		// spec file does import the runtime, and was excluded whole by a
		// constraint this configuration does not satisfy. That is the most
		// likely mistake with variants — running `servo check` without the
		// tags the only spec is gated on — so it gets its own sentence.
		return nil, fmt.Errorf("load: servo runtime package %s is not imported by any file this build configuration (%s) can see — either no spec file exists yet (run `servo init`), or every spec file is gated on a build constraint this configuration does not satisfy, which makes it invisible to this run", graph.ServoPackagePath, cfg.Build.describeConfiguration())
	}

	return &Loaded{Tags: cfg.Build.TagList(), Fset: fset, All: all, ByPath: byPath, Roots: roots, ServoPkg: servoPkg}, nil
}

// isInjectorInAnotherConfiguration reports whether a package that is not an
// injector *here* is one under some other set of build tags, by looking for
// a spec file among the files this configuration excluded.
//
// Without this, a multi-injector module cannot be generated from its root
// with tags at all. Given cmd/api with both a default and a prod spec and
// cmd/worker with only a default one, `servo generate --tags=prod` sees
// cmd/worker's spec excluded by its own `!prod` constraint, so cmd/worker
// is not in the injector list — and its perfectly ordinary pre-generation
// "undefined: New" becomes a fatal module-wide build error pointing at
// nothing the user can act on. The exclusion the caller wants is "packages
// whose New is generated by servo", and that is a property of the package,
// not of the current tag set.
func isInjectorInAnotherConfiguration(p *packages.Package) bool {
	for _, path := range p.IgnoredFiles {
		if !strings.HasSuffix(path, ".go") {
			continue
		}
		// Constraints sit above the package clause, so parsing that far is
		// enough; an unreadable or unparseable file simply is not evidence.
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			continue
		}
		if FileRequiresBuildTag(f, BuildTag) {
			return true
		}
	}
	return false
}

// InjectorsInOtherConfigurations returns the packages that hold a spec
// file this build configuration cannot see, excluding the ones already
// known to be injectors here.
//
// These are the packages that will fail to build under the current tags:
// their main.go calls a New that only a generated file supplies, and this
// configuration generates none for them. Servo cannot fix that — whether
// cmd/worker should have a prod variant, or should itself be gated out of
// the prod build, is the author\'s call — but it should say so, because the
// alternative is a silent `undefined: New` from the compiler much later.
func (l *Loaded) InjectorsInOtherConfigurations(knownInjectorPkgPaths ...string) []string {
	known := make(map[string]bool, len(knownInjectorPkgPaths))
	for _, p := range knownInjectorPkgPaths {
		known[p] = true
	}
	var out []string
	for _, p := range l.All {
		if known[p.PkgPath] || p.Module == nil || !p.Module.Main {
			continue
		}
		if isInjectorInAnotherConfiguration(p) {
			out = append(out, p.PkgPath)
		}
	}
	sort.Strings(out)
	return out
}

// NonInjectorErrors reports load/type errors in any package other than the
// given injector package paths. Errors within an injector package itself
// are deliberately not this function's concern: before the first `servo
// generate`, that package legitimately fails to compile (main.go
// references the not-yet-generated New/App), and that is not a reason to
// refuse to generate — it is the reason to. Variadic so a multi-injector
// module can exclude every known injector at once: otherwise checking
// injector B would trip on injector A's pre-generation "undefined: New".
func (l *Loaded) NonInjectorErrors(injectorPkgPaths ...string) error {
	excluded := make(map[string]bool, len(injectorPkgPaths))
	for _, p := range injectorPkgPaths {
		excluded[p] = true
	}
	var errs []error
	for _, p := range l.All {
		if excluded[p.PkgPath] || len(p.Errors) == 0 {
			continue
		}
		if isInjectorInAnotherConfiguration(p) {
			continue
		}
		for _, e := range p.Errors {
			errs = append(errs, e)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
