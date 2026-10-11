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
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
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
	var overlay map[string][]byte
	if cfg.Build.Overlay != "" {
		o, err := readOverlay(cfg.Build.Overlay)
		if err != nil {
			return nil, err
		}
		overlay = o
	}

	// Two go/packages calls. The first is the import graph alone, no
	// type-checking, and exists to name the packages the second must
	// type-check from source: the main module and every other module in its
	// dependency tree. Those are the packages that can carry servo markers,
	// Include'd marker sets and //servo: directives, all of which live in
	// syntax. The standard library can carry none of them, and it is most
	// of any closure — the runtime package alone pulls in net/http's
	// ~180-package tree. Reading it from export data instead of
	// type-checking it from source is the difference between a load that
	// takes a quarter of a second and one that takes two, and under -race,
	// between a test suite that finishes and one that times out.
	//
	// Everything still shares one type-checking session: go/packages reads
	// export data into the same universe the source packages are checked
	// against, so identity-based checks like types.Implements stay valid
	// across the boundary.
	graphCfg := &packages.Config{
		Dir:        cfg.Dir,
		Mode:       packages.NeedName | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		BuildFlags: cfg.Build.Args(),
		Overlay:    overlay,
	}
	graphRoots, err := packages.Load(graphCfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	rootIDs, fromSource := sourcePatterns(graphRoots)

	// Without NeedDeps, go/packages type-checks from source only the
	// packages the patterns name and reads their dependencies from export
	// data — so every package that needs syntax is passed as a pattern.
	pcfg := &packages.Config{
		Dir: cfg.Dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule | packages.NeedExportFile,
		BuildFlags: cfg.Build.Args(),
		Overlay:    overlay,
	}
	pkgs, err := packages.Load(pcfg, append([]string{pattern}, fromSource...)...)
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	// Roots are the packages the caller's pattern matched — not the
	// dependency packages the second call was also asked for.
	var roots []*packages.Package
	for _, p := range pkgs {
		if rootIDs[p.ID] {
			roots = append(roots, p)
		}
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
	if err := completeExportData(all, fset); err != nil {
		return nil, err
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

// sourcePatterns walks the import graph under roots and returns the root
// IDs plus the import paths of every package in a module — the main module
// and its dependencies alike, the standard library excluded — that the
// roots do not already cover. Those are the packages Load type-checks from
// source, because they are the ones that can carry servo markers: export
// data holds types, not syntax. Sorted, so the go command sees the same
// argument list for the same module every time.
func sourcePatterns(roots []*packages.Package) (rootIDs map[string]bool, extra []string) {
	rootIDs = make(map[string]bool, len(roots))
	for _, r := range roots {
		rootIDs[r.ID] = true
	}
	seen := make(map[string]bool)
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		if p.Module != nil && !rootIDs[p.ID] {
			extra = append(extra, p.PkgPath)
		}
		for _, dep := range p.Imports {
			walk(dep)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	sort.Strings(extra)
	return rootIDs, extra
}

// completeExportData finishes the packages go/packages left partial. It
// reads a dependency's own export data only when a source package imports
// it directly; everything deeper exists in the session only as the
// declarations some direct import happened to reference. The candidate
// scan covers every loaded package — the standard library included, as
// the resolution reference promises — so each package the load left
// incomplete is read from its own export file here, into the same
// universe, which keeps every type object's identity. Packages checked
// from source are complete already and are never re-read.
func completeExportData(all []*packages.Package, fset *token.FileSet) error {
	universe := make(map[string]*types.Package, len(all))
	for _, p := range all {
		if p.Types != nil {
			universe[p.PkgPath] = p.Types
		}
	}
	for _, p := range all {
		if p.Types == nil || p.Types.Complete() || p.TypesInfo != nil || p.ExportFile == "" {
			continue
		}
		if err := readExportData(p, fset, universe); err != nil {
			return fmt.Errorf("load: export data for %s: %w", p.PkgPath, err)
		}
	}
	return nil
}

func readExportData(p *packages.Package, fset *token.FileSet, universe map[string]*types.Package) error {
	f, err := os.Open(p.ExportFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	// `go list -export` still hands back an archive, which only NewReader
	// knows how to open, and go/packages reads these same files the same
	// way. The deprecation is a Go 1.29 notice: once go list writes bare
	// export files, this becomes Read(f, ...) and the directive goes.
	r, err := gcexportdata.NewReader(f) //nolint:staticcheck // archive export files, see above
	if err != nil {
		return err
	}
	// Read completes the existing, partial *types.Package rather than
	// creating a second one, which is the whole point.
	tpkg, err := gcexportdata.Read(r, fset, universe, p.PkgPath)
	if err != nil {
		return err
	}
	p.Types = tpkg
	return nil
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
