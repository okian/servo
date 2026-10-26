package load

import (
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Verification is what Verify found.
type Verification struct {
	// Problems are the errors go build would stop on inside the generated
	// files, one "file:line:col: message" line each.
	Problems []string
	// Unchecked are the packages Verify could not judge, each with the go
	// command's own reason.
	Unchecked []Unchecked
}

// Unchecked is a package Verify skipped, and why: an import go list could
// not resolve — almost always a module whose go.sum line `go mod tidy` has
// not added yet.
type Unchecked struct {
	PkgPath string
	Reason  string
}

// Verify type-checks the packages generated code was written into, with
// that code overlaid, under the configuration the generated files are
// gated for — the user's tags without BuildTag, so the spec file is out
// and the generated file is in, exactly as go build sees them.
//
// Every error positioned inside one of files is a problem, whichever stage
// of the build reports it. Most come from the type checker. Some come from
// go list before the type checker has anything to say — an import of
// another tree's internal/ package, an import cycle a generated import
// closes — and fail `go build` just as surely; they used to be dropped,
// which let generate exit 0 on a file the compiler refused. A package with
// such an error has it reported alone, the way go build reports it: the
// type errors after it are its echo.
//
// A package go list could not fully describe — a module the go.sum has no
// line for yet — cannot be judged: every type error after a failed import
// describes the import, not the generated code, and `go mod tidy` is the
// fix the compiler already names. It is returned in Unchecked, for the
// caller to say so; skipping it silently left generate reporting success on
// a file `go build` would then reject.
func Verify(cfg Config, files map[string][]byte, pkgPaths []string) (Verification, error) {
	overlay := map[string][]byte{}
	if cfg.Build.Overlay != "" {
		o, err := readOverlay(cfg.Build.Overlay)
		if err != nil {
			return Verification{}, err
		}
		maps.Copy(overlay, o)
	}
	maps.Copy(overlay, files)
	// go list and the compiler position errors relative to the directory
	// the go command runs in, which is cfg.Dir.
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return Verification{}, fmt.Errorf("verify: %w", err)
	}
	generated := indexGenerated(dir, files)

	pcfg := &packages.Config{
		Dir: cfg.Dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		BuildFlags: cfg.Build.GeneratedArgs(),
		Overlay:    overlay,
		// The override variant is a _test.go file, so it only exists in
		// the package's test build.
		Tests: true,
	}
	patterns := slices.Sorted(slices.Values(pkgPaths))
	patterns = slices.Compact(patterns)
	pkgs, err := packages.Load(pcfg, patterns...)
	if err == nil && len(pkgs) == 0 && len(patterns) > 0 {
		// The dependencies' types come from export data, which the go
		// command builds, and go/packages reports no failure of that build
		// when it stops before listing a package — see load's handling of
		// the same call. Taken as it came, the empty answer was a
		// verification with nothing in it: generated code that had not been
		// compiled at all, reported as compiling.
		err = exportListFailure(cfg, patterns)
	}
	if err != nil {
		return Verification{}, fmt.Errorf("verify: %w", err)
	}

	var v Verification
	reported, unchecked := map[string]bool{}, map[string]bool{}
	for _, p := range pkgs {
		// With Tests set, a package arrives up to three times — itself, its
		// test build, its external test package — and only the builds that
		// compile a generated file are this function's business.
		mine := generated.in(p)
		if len(mine) == 0 {
			continue
		}
		if reason, ok := unresolvedImport(p); ok {
			if !unchecked[p.PkgPath] {
				unchecked[p.PkgPath] = true
				v.Unchecked = append(v.Unchecked, Unchecked{PkgPath: p.PkgPath, Reason: reason})
			}
			continue
		}
		for _, problem := range generated.problems(p, mine) {
			if !reported[problem] {
				reported[problem] = true
				v.Problems = append(v.Problems, problem)
			}
		}
	}
	return v, nil
}

// unresolvedImport reports whether an import of p could not be resolved —
// a module the go.sum has no line for, a package that is not there at all
// — with the go command's reason. The type checker still runs, but every
// error it then reports is the import's: "could not import" at the import
// line and a cascade after it. The package's own list errors are not the
// signal: go list compiles what it loads, so a plain type error arrives as
// a list error too, with the compiler's output as its message. Nor is an
// import cycle: that is the code's doing, which problems reports as such.
func unresolvedImport(p *packages.Package) (string, bool) {
	// The dependency's own error is the precise one ("missing go.sum entry
	// for module providing package …"), so it is preferred, in a fixed
	// order since Imports is a map.
	for _, path := range slices.Sorted(maps.Keys(p.Imports)) {
		for _, e := range p.Imports[path].Errors {
			if e.Kind == packages.ListError && importCycle(e.Msg) == nil {
				return oneLine(e.Msg), true
			}
		}
	}
	for _, e := range p.Errors {
		if e.Kind == packages.TypeError && strings.HasPrefix(e.Msg, "could not import ") && importCycle(e.Msg) == nil {
			return oneLine(e.Msg), true
		}
	}
	return "", false
}

// problems is every error in p that go build would stop on inside one of
// the generated files mine, as "file:line:col: message" lines.
func (g generatedFiles) problems(p *packages.Package, mine []string) []string {
	// go list's own errors first, as go build reports them: a package
	// that has one never reaches the compiler.
	var out []string
	for _, e := range errorsAround(p) {
		if e.Kind != packages.ListError || isCompilerOutput(e.Msg) {
			continue
		}
		if line, ok := g.listProblem(p, e, mine); ok {
			out = append(out, line)
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, e := range p.Errors {
		if e.Kind != packages.TypeError && e.Kind != packages.ParseError {
			continue
		}
		file := errorFile(e.Pos)
		if path, ok := g.lookup(file); ok {
			out = append(out, path+strings.TrimPrefix(e.Pos, file)+": "+e.Msg)
		}
	}
	if len(out) > 0 {
		return out
	}
	// The compiler's own report, which go list attaches as one more list
	// error: the last word on anything the type checker let through.
	for _, e := range p.Errors {
		if e.Kind == packages.ListError && isCompilerOutput(e.Msg) {
			out = append(out, g.compilerProblems(e.Msg)...)
		}
	}
	return out
}

// errorsAround is p's own errors and those of the packages it imports
// directly, which is where go list may file an import cycle p is part of.
func errorsAround(p *packages.Package) []packages.Error {
	errs := slices.Clone(p.Errors)
	for _, path := range slices.Sorted(maps.Keys(p.Imports)) {
		for _, e := range p.Imports[path].Errors {
			if importCycle(e.Msg) != nil {
				errs = append(errs, e)
			}
		}
	}
	return errs
}

// listProblem renders a go list error as a problem when the generated
// files are its cause: positioned inside one of them, or an import cycle
// one of them closes.
func (g generatedFiles) listProblem(p *packages.Package, e packages.Error, mine []string) (string, bool) {
	if stack := importCycle(e.Msg); stack != nil {
		return g.cycleProblem(p, stack, mine)
	}
	file := errorFile(e.Pos)
	path, ok := g.lookup(file)
	if !ok {
		return "", false
	}
	return path + strings.TrimPrefix(e.Pos, file) + ": " + oneLine(e.Msg), true
}

// cycleProblem places an import cycle at the generated import that closes
// it — the import of the package that follows p in the cycle — or reports
// it is not the generated files' doing. go list positions a cycle nowhere,
// which is how it came to be dropped along with every other unpositioned
// error, leaving only the type checker's confusing echoes of it.
func (g generatedFiles) cycleProblem(p *packages.Package, stack []string, mine []string) (string, bool) {
	at := slices.Index(stack, p.PkgPath)
	if at < 0 {
		return "", false
	}
	// go list closes the stack ([a b a]); the type checker does not ([a b]).
	next := stack[(at+1)%len(stack)]
	cycle := slices.Clone(stack)
	if cycle[0] != cycle[len(cycle)-1] {
		cycle = append(cycle, cycle[0])
	}
	for _, path := range mine {
		if pos, ok := importPosition(path, g.content[path], next); ok {
			return fmt.Sprintf("%s: import cycle not allowed: %s", pos, strings.Join(cycle, " → ")), true
		}
	}
	return "", false
}

// importPosition is where the generated file at path imports pkgPath.
func importPosition(path string, content []byte, pkgPath string) (token.Position, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ImportsOnly)
	if err != nil {
		return token.Position{}, false
	}
	for _, spec := range f.Imports {
		if imported, err := strconv.Unquote(spec.Path.Value); err == nil && imported == pkgPath {
			return fset.Position(spec.Pos()), true
		}
	}
	return token.Position{}, false
}

// compilerProblems picks the lines of a compiler report positioned inside
// a generated file, each with the indented lines that continue it.
func (g generatedFiles) compilerProblems(report string) []string {
	var out []string
	keep := false
	for _, line := range strings.Split(report, "\n") {
		if m := compilerLine.FindStringSubmatch(line); m != nil {
			path, ok := g.lookup(m[1])
			keep = ok
			if ok {
				out = append(out, path+m[2])
			}
			continue
		}
		if keep && strings.HasPrefix(line, "\t") {
			out[len(out)-1] += "\n" + line
		}
	}
	return out
}

// compilerLine is one positioned line of a compiler report: the file, then
// everything from the position on.
var compilerLine = regexp.MustCompile(`^(.+?)(:\d+(?::\d+)?: .*)$`)

// isCompilerOutput reports whether a list error is the compiler's report,
// which go list attaches whole, headed with the package path.
func isCompilerOutput(msg string) bool { return strings.HasPrefix(msg, "# ") }

// cycleStack matches both ways an import cycle is spelled: go list's
// "import cycle not allowed: import stack: [a b a]" and the type checker's
// "could not import b (import cycle: [a b])".
var cycleStack = regexp.MustCompile(`import (?:cycle not allowed: import stack|cycle): \[([^\]]*)\]`)

// importCycle is the package path stack of an import-cycle error, or nil
// for any other error.
func importCycle(msg string) []string {
	if m := cycleStack.FindStringSubmatch(msg); m != nil {
		return strings.Fields(m[1])
	}
	return nil
}

// oneLine folds the go command's multi-line messages ("…; to add it:\n\tgo
// get …") onto one line, so each problem stays one line of the report.
func oneLine(msg string) string { return strings.Join(strings.Fields(msg), " ") }

// generatedFiles finds the generated file an error position names. The
// type checker positions errors by absolute path, go list and the compiler
// by a path relative to the go command's working directory — and on a
// machine where that directory sits under a symlink, as /tmp and /var do
// on macOS, one file arrives under two absolute names, so paths are
// compared once more with links resolved.
type generatedFiles struct {
	dir     string            // the go command's working directory, absolute
	content map[string][]byte // the generated files, keyed as given
	byPath  map[string]string // cleaned path → key
	byReal  map[string]string // path with links resolved → key
}

func indexGenerated(dir string, files map[string][]byte) generatedFiles {
	g := generatedFiles{dir: dir, content: files, byPath: map[string]string{}, byReal: map[string]string{}}
	for path := range files {
		g.byPath[filepath.Clean(path)] = path
		if real, ok := resolveLinks(path); ok {
			g.byReal[real] = path
		}
	}
	return g
}

// lookup returns the generated file named by file, as Verify was given it.
func (g generatedFiles) lookup(file string) (string, bool) {
	if file == "" {
		return "", false
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(g.dir, file)
	}
	if path, ok := g.byPath[filepath.Clean(file)]; ok {
		return path, true
	}
	real, ok := resolveLinks(file)
	if !ok {
		return "", false
	}
	path, ok := g.byReal[real]
	return path, ok
}

// resolveLinks resolves the symlinks in path's directory. The file itself
// is left alone because it need not exist: an overlaid file is only ever
// on disk in the go command's imagination.
func resolveLinks(path string) (string, bool) {
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, filepath.Base(path)), true
}

// in returns the generated files compiled into p.
func (g generatedFiles) in(p *packages.Package) []string {
	var mine []string
	for _, file := range slices.Concat(p.CompiledGoFiles, p.GoFiles) {
		if path, ok := g.lookup(file); ok && !slices.Contains(mine, path) {
			mine = append(mine, path)
		}
	}
	return mine
}

// errorPos is the "file:line:col" or "file:line" shape packages.Error.Pos
// takes; the file may itself contain colons on Windows, which is why the
// match is anchored on the trailing numbers rather than the first colon.
var errorPos = regexp.MustCompile(`^(.*?):\d+(?::\d+)?$`)

// errorFile is the file half of a packages.Error position, or "" for an
// error with no position.
func errorFile(pos string) string {
	if m := errorPos.FindStringSubmatch(pos); m != nil {
		return m[1]
	}
	return ""
}
