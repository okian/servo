// Package load wraps golang.org/x/tools/go/packages to load the whole main
// module and every package it transitively imports in one type-checking
// session (so identity-based checks like types.Implements are valid across
// package boundaries), and to locate + parse the servo.Build(...) spec
// calls.
package load

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// BuildTag is the build constraint that gates the spec file. Loading always
// activates it, so `servo generate` sees the spec file (read as syntax,
// never executed) and never the previously generated output for the same
// injector.
const BuildTag = "servoinject"

// Config controls what Load reads and which injectors a command processes.
type Config struct {
	// Dir selects the injectors a command processes: the packages `./...`
	// matches from it, which is Dir itself and every directory below. ""
	// means the current directory. It selects and nothing more — the whole
	// main module is loaded whichever injectors it picks; see Load.
	Dir string
	// Build are the go command build flags passed through to
	// go/packages, which decide which files and packages the load can
	// see at all. The zero value loads the default configuration.
	Build BuildFlags
	// Scope is which modules count as the main module. The zero value is
	// ScopeModule.
	Scope ModuleScope
	// CLIVersion is the running servo's version, for the messages that set
	// it beside the version of the runtime package the module resolves. ""
	// leaves it out.
	CLIVersion string
}

// ModuleScope is `--scope`: which modules a run treats as the main module —
// the modules whose packages are searched for interface implementations,
// //servo: directives and //servo:config types, and whose directories
// servo may write a generated file into.
type ModuleScope string

const (
	// ScopeModule is the module Dir belongs to, and no other: the module
	// the go command builds when no go.work is in play. It is the default
	// because it is what CI usually builds — `GOWORK=off` — and a generated
	// file has to be the one a fresh checkout regenerates.
	ScopeModule ModuleScope = "module"
	// ScopeWorkspace is every module go.work lists, which the go command
	// itself treats as main modules: what servo did before --scope existed.
	ScopeWorkspace ModuleScope = "workspace"
)

// Validate rejects a scope servo has no meaning for. The zero value is
// ScopeModule.
func (s ModuleScope) Validate() error {
	switch s {
	case "", ScopeModule, ScopeWorkspace:
		return nil
	}
	return fmt.Errorf("servo: --scope=%s: the scope is either %s (the module --dir is in) or %s (every module go.work lists)", s, ScopeModule, ScopeWorkspace)
}

// Loaded is the whole main module and every package it imports,
// deduplicated and sharing one type-checking session.
type Loaded struct {
	// Tags is the canonical user tag set this load ran under (BuildTag
	// excluded, since it is always present). It identifies the variant:
	// the generated file's name and its build constraint both derive
	// from it.
	Tags []string
	Fset *token.FileSet
	// All is every loaded package, deduplicated, in the same order on
	// every run — the candidate scan's universe.
	All    []*packages.Package
	ByPath map[string]*packages.Package
	// Selected are the packages Config.Dir selects. An injector is
	// processed only if it is one of them; every other package is here
	// for what it declares.
	Selected []*packages.Package
	ServoPkg *packages.Package
	// Note is one line for stderr about how the load had to run, or "" —
	// today only the slower fallback taken when the standard library's
	// export data cannot be read, which should not pass in silence.
	Note string

	// mainModules are the paths of the modules InMainModule accepts, nil
	// on a Loaded built by hand, where Module.Main alone decides.
	mainModules map[string]bool
	// dir is Config.Dir the way a message names it.
	dir        string
	cliVersion string
	// scan memoizes the candidate scan; nil on a Loaded built by hand.
	scan *candidateScan
	// specs memoizes ModuleSpecs; nil on a Loaded built by hand.
	specs *moduleSpecs
}

// Load reads the main module Config.Dir belongs to — all of it, plus every
// package it imports — not only what Dir's own packages import.
//
// Dir decides which injectors are processed, never what they can see. An
// interface's implementations are wherever the module put them, and the
// //servo: routes and //servo:config types belong to the module rather than
// to whichever injector is being generated. A load narrowed to one
// injector's imports auto-bound the one implementation that injector
// happened to import where the module root saw two, and served none of the
// routes in packages nothing imported yet — and `go generate` runs servo
// from the injector's own directory, so the narrowed load was what the
// scaffolded directive got, while `servo check` from the module root
// disagreed with the file it wrote.
func Load(cfg Config) (*Loaded, error) {
	if err := cfg.Build.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.Scope.Validate(); err != nil {
		return nil, err
	}
	if err := checkDir(cfg.Dir); err != nil {
		return nil, err
	}
	var overlay map[string][]byte
	if cfg.Build.Overlay != "" {
		o, err := readOverlay(cfg.Build.Overlay)
		if err != nil {
			return nil, err
		}
		overlay = o
	}

	sel, err := selectPackages(cfg, overlay)
	if err != nil {
		return nil, err
	}
	l, err := load(cfg, overlay, sel, false)
	var unreadable *exportDataError
	if !errors.As(err, &unreadable) {
		return l, err
	}
	// The go command wrote that export data moments ago, so failing to
	// decode it is not damage: the toolchain's format is newer than the
	// reader in this binary's golang.org/x/tools. Every package can still be
	// type-checked from source — the loader's only mode before it read
	// export data at all — so a new Go release costs speed, not every
	// command, until the reader catches up.
	unreadable.tools, unreadable.goVersion = toolsVersion(), goVersion(cfg.Dir)
	l, err = load(cfg, overlay, sel, true)
	if err == nil {
		err = stdlibError(l.All)
	}
	if err != nil {
		unreadable.fromSource = err
		return nil, unreadable
	}
	l.Note = unreadable.note()
	return l, nil
}

// checkDir refuses a Dir that is not a directory before the go command is
// asked anything. The go command runs with Dir as its working directory,
// and a missing one fails as `fork/exec /usr/local/go/bin/go: no such file
// or directory` — naming the go binary, which is there, instead of the
// directory, which is not.
func checkDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("servo: directory %s does not exist — check --dir", dir)
	case err != nil:
		return fmt.Errorf("servo: %w", err)
	case !info.IsDir():
		return fmt.Errorf("servo: %s is a file, not a directory — --dir names the directory to scan from", dir)
	}
	return nil
}

// selection is what Config.Dir selects: the import paths `./...` matches
// from it, and the main modules those packages belong to.
type selection struct {
	paths   map[string]bool
	modules []*packages.Module // sorted by path
}

// selectPackages asks the go command what `./...` matches from Dir, rather
// than working it out from directory names, so vendor, testdata, nested
// modules and build constraints are applied exactly as everywhere else in
// the toolchain. It is also where a directory the go command cannot load
// from at all is reported, in the go command's own words: the alternative
// was a load of nothing, which reached the user as a missing servo runtime
// and the advice to run `servo init`.
func selectPackages(cfg Config, overlay map[string][]byte) (*selection, error) {
	pkgs, err := packages.Load(&packages.Config{
		Dir:        cfg.Dir,
		Mode:       packages.NeedName | packages.NeedModule,
		BuildFlags: cfg.Build.Args(),
		Overlay:    overlay,
	}, "./...")
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	sel := &selection{paths: make(map[string]bool, len(pkgs))}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.PkgPath == "./..." && len(p.Errors) > 0 {
			// Not a package: the go command's report about the pattern
			// itself.
			return nil, patternError(displayDir(cfg.Dir), p.Errors[0].Msg)
		}
		sel.paths[p.PkgPath] = true
		if m := p.Module; m != nil && m.Main && !seen[m.Path] {
			seen[m.Path] = true
			sel.modules = append(sel.modules, m)
		}
	}
	if len(sel.paths) == 0 {
		return nil, fmt.Errorf("servo: no Go package in %s or below is visible to this build configuration (%s), so there is no injector to process — run `servo init` there to scaffold one, or point --dir at an injector's directory", displayDir(cfg.Dir), cfg.Build.describeConfiguration())
	}
	if len(sel.modules) == 0 {
		return nil, fmt.Errorf("servo: the packages in %s belong to no main module — run servo from inside your own module", displayDir(cfg.Dir))
	}
	slices.SortFunc(sel.modules, func(a, b *packages.Module) int { return strings.Compare(a.Path, b.Path) })
	return sel, nil
}

// patternError is the go command's refusal to match `./...` from dir, with
// the one step that gets past it. The go command's message is kept
// verbatim: it is accurate, and it is what a search for the problem finds.
func patternError(dir, msg string) error {
	switch {
	case strings.Contains(msg, "does not contain main module"):
		return fmt.Errorf("servo: %s is not inside a Go module (go: %s) — create one with `go mod init <module path>`, then add servo to it with `go get -tool %s`", dir, msg, toolPackage)
	case strings.Contains(msg, "does not contain modules listed in go.work"):
		return fmt.Errorf("servo: %s is in a go.work workspace but inside none of the modules it lists (go: %s) — run servo from one of those modules, or point --dir at one; --scope=%s lets that run see the others", dir, msg, ScopeWorkspace)
	}
	return fmt.Errorf("load: %s", msg)
}

// toolPackage is the generator's own package — what `go get -tool` records
// in go.mod, adding the servo module to the requirements with it.
const toolPackage = "github.com/okian/servo/v3/cmd/servo"

// displayDir is Config.Dir the way a message should name it.
func displayDir(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// load is one attempt at Load. The standard library comes from the
// compiler's export data unless stdlibFromSource is set, in which case it
// is type-checked from source like everything else.
func load(cfg Config, overlay map[string][]byte, sel *selection, stdlibFromSource bool) (*Loaded, error) {
	newConfig := func(mode packages.LoadMode) *packages.Config {
		return &packages.Config{Dir: cfg.Dir, Mode: mode, BuildFlags: cfg.Build.Args(), Overlay: overlay}
	}

	// The whole main module, named by the root directory the go command
	// reported for it — its own view of the path, symlinks resolved or
	// not, so the pattern means to the next call what it meant to the
	// last. Under --scope=workspace it is every module go.work lists, which
	// is exactly what the go command's `work` pattern matches. "./..."
	// stays as well: a Dir under testdata, say, is outside a module's own
	// `...` and still what the caller asked for.
	patterns := []string{"./..."}
	if cfg.Scope == ScopeWorkspace {
		patterns = append(patterns, "work")
	} else {
		for _, m := range sel.modules {
			if m.Dir != "" {
				patterns = append(patterns, filepath.Join(m.Dir, "..."))
			}
		}
	}

	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
		packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule
	var pkgs []*packages.Package
	var err error
	if stdlibFromSource {
		pkgs, err = packages.Load(newConfig(mode|packages.NeedDeps), patterns...)
	} else {
		// Two go/packages calls. The first is the import graph alone, no
		// type-checking, and exists to name the packages the second must
		// type-check from source: the main module and every other module in
		// its dependency tree. Those are the packages that can carry servo
		// markers, Include'd marker sets and //servo: directives, all of
		// which live in syntax. The standard library can carry none of
		// them, and it is most of any closure — the runtime package alone
		// pulls in net/http's ~180-package tree. Reading it from export data
		// instead of type-checking it from source is the difference between
		// a load that takes a quarter of a second and one that takes two,
		// and under -race, between a test suite that finishes and one that
		// times out.
		//
		// Everything still shares one type-checking session: go/packages
		// reads export data into the same universe the source packages are
		// checked against, so identity-based checks like types.Implements
		// stay valid across the boundary.
		var importGraph []*packages.Package
		importGraph, err = packages.Load(newConfig(packages.NeedName|packages.NeedImports|packages.NeedDeps|packages.NeedModule), patterns...)
		if err != nil {
			return nil, fmt.Errorf("load: %w", err)
		}
		// Without NeedDeps, go/packages type-checks from source only the
		// packages the patterns name and reads their dependencies from
		// export data — so every package that needs syntax is passed as a
		// pattern.
		args := append(patterns, sourcePatterns(importGraph)...)
		pkgs, err = packages.Load(newConfig(mode|packages.NeedExportFile), args...)
		if err == nil && len(pkgs) == 0 {
			// Export data means a build, and go/packages reports no failure
			// of this call: it takes one for a package's own build error,
			// which that package carries, and returns what the go command
			// printed — nothing, when it stopped before listing a package.
			// ./... matched packages a moment ago, so an empty answer is
			// that failure, and left alone it read as a runtime nothing
			// imports, with the advice to run `servo init`.
			err = exportListFailure(cfg, args)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load: %w", err)
	}
	// Package errors are not fatal here: on a fresh checkout, before the
	// first `servo generate` has ever run, the injector's own package
	// legitimately has an "undefined: New" (or similar) error, since main.go
	// references the not-yet-generated output. go/types still populates
	// Uses/Instances for everything that DOES type-check (the
	// servo.Build(...) call itself does not depend on New/App at all), so
	// FindSpecs works fine regardless. NonInjectorErrors is the narrower,
	// correct check: real problems in packages other than the injectors are
	// still fatal.

	// Walked in import-path order, roots and imports alike. Map order made
	// All — and with it the package an export-data failure was reported
	// against — differ from one run to the next.
	slices.SortFunc(pkgs, func(a, b *packages.Package) int { return strings.Compare(a.PkgPath, b.PkgPath) })
	all := make([]*packages.Package, 0, len(pkgs))
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
		for _, path := range slices.Sorted(maps.Keys(p.Imports)) {
			walk(p.Imports[path])
		}
	}
	for _, p := range pkgs {
		walk(p)
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
	if err := runtimeLoadError(servoPkg); err != nil {
		return nil, err
	}
	if !stdlibFromSource {
		if err := completeExportData(all, fset); err != nil {
			return nil, err
		}
	}

	mainModules := map[string]bool{}
	if cfg.Scope == ScopeWorkspace {
		for _, p := range all {
			if p.Module != nil && p.Module.Main {
				mainModules[p.Module.Path] = true
			}
		}
	} else {
		for _, m := range sel.modules {
			mainModules[m.Path] = true
		}
	}
	var selected []*packages.Package
	for _, p := range pkgs {
		if sel.paths[p.PkgPath] {
			selected = append(selected, p)
		}
	}
	return &Loaded{
		Tags:        cfg.Build.TagList(),
		Fset:        fset,
		All:         all,
		ByPath:      byPath,
		Selected:    selected,
		ServoPkg:    servoPkg,
		mainModules: mainModules,
		dir:         displayDir(cfg.Dir),
		cliVersion:  cfg.CLIVersion,
		scan:        &candidateScan{},
		specs:       &moduleSpecs{},
	}, nil
}

// exportListFailure is the go command's reason it listed no packages when
// asked for export data — a build cache turned off with GOCACHE=off, a
// -toolexec that fails. go/packages drops the go command's stderr for that
// call, so the same question is asked again here, directly, to read it.
func exportListFailure(cfg Config, patterns []string) error {
	args := append([]string{"list", "-e", "-export=true", "-deps=true", "-f", "{{.ImportPath}}"}, cfg.Build.Args()...)
	if cfg.Build.Overlay != "" {
		args = append(args, "-overlay="+cfg.Build.Overlay)
	}
	cmd := exec.Command("go", append(append(args, "--"), patterns...)...)
	cmd.Dir = cfg.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		reason := oneLineMsg(stderr.String())
		if reason == "" {
			reason = err.Error()
		}
		return fmt.Errorf("go list -export failed: %s", reason)
	}
	return errors.New("go list -export listed no packages, then listed them when asked again — run servo again")
}

// runtimeLoadError is the go command's reason the servo runtime package did
// not load, with the step that fixes it. A module that has not added servo
// yet — `servo init` run from a globally installed binary, before `go get`
// — imports a package no requirement provides; the go command says exactly
// that, and servo used to drop it and fail later, saying the runtime
// declared none of the capability interfaces.
func runtimeLoadError(servoPkg *packages.Package) error {
	for _, e := range servoPkg.Errors {
		if e.Kind != packages.ListError {
			continue
		}
		msg := strings.TrimSpace(e.Msg)
		where := ""
		if e.Pos != "" && e.Pos != "-" {
			where = e.Pos + ": "
		}
		switch {
		case strings.Contains(msg, "no required module provides package"):
			return fmt.Errorf("servo: %sthe module does not require %s yet (go: %s) — add it with `go get -tool %s`, which also records the generator in go.mod, so `go tool servo` runs the version the module builds against", where, path.Dir(graph.ServoPackagePath), oneLineMsg(msg), toolPackage)
		case strings.Contains(msg, "missing go.sum entry"):
			return fmt.Errorf("servo: %sgo.sum has no entry for %s (go: %s) — run `go mod tidy`", where, path.Dir(graph.ServoPackagePath), oneLineMsg(msg))
		}
		return fmt.Errorf("servo: %sthe servo runtime package %s did not load: %s", where, graph.ServoPackagePath, oneLineMsg(msg))
	}
	return nil
}

// oneLineMsg folds a multi-line go command message onto one line, the
// shape every servo diagnostic keeps.
func oneLineMsg(msg string) string {
	return strings.Join(strings.Fields(msg), " ")
}

// sourcePatterns walks the import graph under roots and returns the import
// paths of every package in a module — the main module and its
// dependencies alike, the standard library excluded — that the roots do
// not already cover. Those are the packages Load type-checks from source,
// because they are the ones that can carry servo markers: export data
// holds types, not syntax. Sorted, so the go command sees the same
// argument list for the same module every time.
func sourcePatterns(roots []*packages.Package) []string {
	isRoot := make(map[string]bool, len(roots))
	for _, r := range roots {
		isRoot[r.ID] = true
	}
	seen := make(map[string]bool)
	var extra []string
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		if p.Module != nil && !isRoot[p.ID] {
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
	return extra
}

// InMainModule reports whether p belongs to the main module this load
// resolves against: under --scope=module the module Config.Dir is in, and
// nothing else even when a go.work lists more; under --scope=workspace every
// module go.work lists.
//
// It is the one test every "is this the user's own code" question goes
// through: whose packages structural search may bind, whose //servo: lines
// and //servo:config types are read, where a servo.Build call is an
// injector. Module.Main answers the go command's question instead, and in
// a workspace it is true for every listed module — so a sibling module's
// implementation was auto-bound and a config companion was written into a
// sibling's directory, and both vanished the moment CI built the same
// commit with GOWORK=off.
func (l *Loaded) InMainModule(p *packages.Package) bool {
	if p == nil || p.Module == nil || !p.Module.Main {
		return false
	}
	return l.mainModules == nil || l.mainModules[p.Module.Path]
}

// MainPackages is every loaded package InMainModule accepts, in All's
// order.
func (l *Loaded) MainPackages() []*packages.Package {
	var out []*packages.Package
	for _, p := range l.All {
		if l.InMainModule(p) {
			out = append(out, p)
		}
	}
	return out
}

// RuntimeVersion is the version of the servo runtime package the module
// resolves — what go.mod requires, after version selection — or "" when
// there is no release to name: the runtime replaced by another directory
// or module, or the module being servo itself.
func (l *Loaded) RuntimeVersion() string {
	if l.ServoPkg == nil || l.ServoPkg.Module == nil {
		return ""
	}
	m := l.ServoPkg.Module
	if m.Main || m.Replace != nil || m.Version == "" || m.Version == "(devel)" {
		return ""
	}
	return m.Version
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
//
// A package go/packages itself could not read is among the partial ones,
// so it fails here too, in All's order — which makes the package a decode
// failure is reported against the same on every run.
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
		if err := readExport(p, fset, universe); err != nil {
			// A file that will not open is a build cache problem, not a
			// format the reader does not know, and type-checking from
			// source would only hide it.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return fmt.Errorf("load: export data for %s: %w", p.PkgPath, err)
			}
			return &exportDataError{pkg: p.PkgPath, err: err}
		}
	}
	return nil
}

// readExport is readExportData, unless a test stands in for a go command
// whose export data this binary's golang.org/x/tools cannot decode.
var readExport = readExportData

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

// isInjector reports whether p is an injector in this configuration: a
// package with a servo.Build call in a spec file it can see. Whether this
// run processes it is beside the point — the whole module is loaded, and a
// sibling injector's pre-generation "undefined: New" is as legitimate as
// the processed one's.
func isInjector(p *packages.Package) bool {
	if p.TypesInfo == nil {
		return false
	}
	for _, f := range p.Syntax {
		// Only a spec file can hold a Build call FindSpecs accepts, and
		// the gate is a comment, far cheaper to read than the file.
		if !FileRequiresBuildTag(f, BuildTag) {
			continue
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isBuildCall(p, call) {
				found = true
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
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

// InjectorsInOtherConfigurations returns the packages Config.Dir selects
// that hold a spec file this build configuration cannot see, excluding the
// ones already known to be injectors here.
//
// These are the packages that will fail to build under the current tags:
// their main.go calls a New that only a generated file supplies, and this
// configuration generates none for them. Servo cannot fix that — whether
// cmd/worker should have a prod variant, or should itself be gated out of
// the prod build, is the author\'s call — but it should say so, because the
// alternative is a silent `undefined: New` from the compiler much later.
//
// Only the selection is asked about, like every other per-injector report:
// an injector outside Dir is one the caller chose not to look at.
func (l *Loaded) InjectorsInOtherConfigurations(knownInjectorPkgPaths ...string) []string {
	known := make(map[string]bool, len(knownInjectorPkgPaths))
	for _, p := range knownInjectorPkgPaths {
		known[p] = true
	}
	var out []string
	for _, p := range l.selectedPackages() {
		if known[p.PkgPath] || !l.InMainModule(p) {
			continue
		}
		if isInjectorInAnotherConfiguration(p) {
			out = append(out, p.PkgPath)
		}
	}
	sort.Strings(out)
	return out
}

// selectedPackages is Selected, or every package on a Loaded built by
// hand, which selects nothing in particular.
func (l *Loaded) selectedPackages() []*packages.Package {
	if l.Selected == nil && l.mainModules == nil {
		return l.All
	}
	return l.Selected
}

// NonInjectorErrors reports load/type errors in any package other than an
// injector's own, and other than the errors an injector's not-yet-generated
// code causes in the packages that use it.
//
// Errors within an injector package are deliberately not this function's
// concern: before the first `servo generate`, that package legitimately
// fails to compile (main.go references the not-yet-generated New/App), and
// that is not a reason to refuse to generate — it is the reason to. Every
// injector in the load is excluded, whichever ones this run processes:
// checking injector B must not trip on injector A's pre-generation
// "undefined: New" just because --dir selected B alone. The variadic paths
// are excluded on top, for a caller that already knows its injectors.
//
// The same holds one step further out. An injector in an importable
// package — the wiring of a binary whose main.go calls `wiring.New` — is
// loaded with the servoinject tag, which hides the generated file, so the
// package calling it fails to type-check exactly as the injector's own
// main.go would. Those errors are excused too, and only those: a selector
// naming an injector package's member that does not exist yet. Anything
// else wrong in that package is still reported, and so is everything a
// plain `go build` would refuse once the file exists.
func (l *Loaded) NonInjectorErrors(injectorPkgPaths ...string) error {
	broken := false
	for _, p := range l.All {
		if len(p.Errors) > 0 {
			broken = true
			break
		}
	}
	if !broken {
		return nil
	}

	injectors := make(map[string]bool, len(injectorPkgPaths))
	for _, p := range injectorPkgPaths {
		injectors[p] = true
	}
	for _, p := range l.All {
		// A dependency module's injector counts too: the tag hides its
		// generated file from every package that imports it, wherever it
		// lives. Asking about the excluded files is a parse per file, so
		// only the main module, where a spec can be for this configuration
		// or another, is asked that.
		if injectors[p.PkgPath] || p.Module == nil {
			continue
		}
		if isInjector(p) || (l.InMainModule(p) && isInjectorInAnotherConfiguration(p)) {
			injectors[p.PkgPath] = true
		}
	}

	var errs []error
	for _, p := range l.All {
		if injectors[p.PkgPath] || len(p.Errors) == 0 {
			continue
		}
		pending := pendingGeneration(p, injectors)
		for _, e := range p.Errors {
			if pending(e) {
				continue
			}
			errs = append(errs, e)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// pendingGeneration reports which of p's errors exist only because an
// injector p imports has not been generated yet: the type checker's
// "undefined: wiring.New" at a selector whose package is that injector,
// and go list's echo of the same failure — it compiles what it loads, so
// the type errors arrive a second time as the compiler's output, which is
// excused only when every one of them is.
func pendingGeneration(p *packages.Package, injectors map[string]bool) func(packages.Error) bool {
	none := func(packages.Error) bool { return false }
	importsInjector := false
	for path := range p.Imports {
		if injectors[path] {
			importsInjector = true
			break
		}
	}
	if !importsInjector || p.TypesInfo == nil || len(p.TypeErrors) == 0 {
		return none
	}
	pending := map[string]bool{}
	every := true
	for _, te := range p.TypeErrors {
		if namesUngeneratedMember(p, te, injectors) {
			pending[te.Fset.Position(te.Pos).String()+"\x00"+te.Msg] = true
		} else {
			every = false
		}
	}
	return func(e packages.Error) bool {
		switch e.Kind {
		case packages.TypeError:
			return pending[e.Pos+"\x00"+e.Msg]
		case packages.ListError:
			return every && strings.HasPrefix(e.Msg, "# ")
		}
		return false
	}
}

// namesUngeneratedMember reports whether te is "undefined: x.Name" at a
// selector whose x is an imported injector package — a member only that
// injector's generated file declares, or one that does not exist at all,
// which the build reports once the file is there.
func namesUngeneratedMember(p *packages.Package, te types.Error, injectors map[string]bool) bool {
	if !strings.HasPrefix(te.Msg, "undefined: ") {
		return false
	}
	for _, f := range p.Syntax {
		if te.Pos < f.Pos() || te.Pos >= f.End() {
			continue
		}
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			if found || n == nil || te.Pos < n.Pos() || te.Pos >= n.End() {
				return false
			}
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Pos() != te.Pos {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok {
				if pkgName, ok := p.TypesInfo.Uses[x].(*types.PkgName); ok && injectors[pkgName.Imported().Path()] {
					found = true
				}
			}
			return false
		})
		return found
	}
	return false
}

// candidateScan is graph.ScanCandidates over the whole load, run once
// however many injectors the load holds.
type candidateScan struct {
	once     sync.Once
	accepted []*graph.Provider
	rejected []graph.Rejected
}

// Candidates is graph.ScanCandidates(l.All, injector.PkgPath) — the same
// providers and rejections, in the same order — computed from one scan of
// the load shared by every injector in it.
//
// The injector only changes the verdict on its own package: there, and
// nowhere else, unexported functions are candidates and a package main is
// not someone else's. So the load is scanned once as if no package were
// the injector, and each injector's own package is scanned again on its own
// and spliced in. Scanning everything per injector cost a module with 44 of
// them close to a second and a third of a gigabyte.
func (l *Loaded) Candidates(injector *packages.Package) ([]*graph.Provider, []graph.Rejected) {
	if l.scan == nil {
		return graph.ScanCandidates(l.All, injector.PkgPath)
	}
	l.scan.once.Do(func() {
		l.scan.accepted, l.scan.rejected = graph.ScanCandidates(l.All, "")
	})
	own, ownRejected := graph.ScanCandidates([]*packages.Package{injector}, injector.PkgPath)

	accepted := make([]*graph.Provider, 0, len(l.scan.accepted)+len(own))
	for _, p := range l.scan.accepted {
		if p.Pkg != injector.PkgPath {
			accepted = append(accepted, p)
		}
	}
	accepted = append(accepted, own...)
	rejected := make([]graph.Rejected, 0, len(l.scan.rejected)+len(ownRejected))
	for _, r := range l.scan.rejected {
		if r.Pkg != injector.PkgPath {
			rejected = append(rejected, r)
		}
	}
	rejected = append(rejected, ownRejected...)
	// The order graph.ScanCandidates sorts by. Every entry has a position
	// of its own, so the order is total and the splice cannot show.
	sort.Slice(accepted, func(i, j int) bool { return graph.ComparePos(accepted[i].Pos, accepted[j].Pos) < 0 })
	sort.Slice(rejected, func(i, j int) bool { return graph.ComparePos(rejected[i].Pos, rejected[j].Pos) < 0 })
	return accepted, rejected
}
