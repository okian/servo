// Package load wraps golang.org/x/tools/go/packages to load the main
// module and every transitively imported package in one type-checking
// session (so identity-based checks like types.Implements are valid across
// package boundaries), and to locate + parse the servo.Build(...) spec
// call.
package load

import (
	"errors"
	"fmt"
	"go/token"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v2/internal/graph"
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
}

// Loaded is every package reachable from Pattern, deduplicated and sharing
// one type-checking session.
type Loaded struct {
	Fset     *token.FileSet
	All      []*packages.Package // every loaded package, deduplicated — the candidate scan's universe
	ByPath   map[string]*packages.Package
	Roots    []*packages.Package // packages matching the input pattern
	ServoPkg *packages.Package
}

func Load(cfg Config) (*Loaded, error) {
	pattern := cfg.Pattern
	if pattern == "" {
		pattern = "./..."
	}
	pcfg := &packages.Config{
		Dir: cfg.Dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
		BuildFlags: []string{"-tags=" + BuildTag},
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
		return nil, fmt.Errorf("load: servo runtime package %s not found among loaded packages (the spec file must import it to call Build/Root/Bind)", graph.ServoPackagePath)
	}

	return &Loaded{Fset: fset, All: all, ByPath: byPath, Roots: roots, ServoPkg: servoPkg}, nil
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
		if excluded[p.PkgPath] {
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
