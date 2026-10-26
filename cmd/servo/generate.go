package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// generateOptions are the flags generate takes beyond the load
// configuration every loading command shares.
type generateOptions struct {
	// verbose lists every file the run wrote, found already current, or
	// removed (-v).
	verbose bool
	// invoked is the loading flags the run was given, which any command it
	// suggests repeats.
	invoked invocation
}

// runGenerate processes every injector found within dir's scope. A module
// with one injector behaves exactly as before; a module with several (a
// monorepo's cmd/api, cmd/worker, cmd/migrator) gets all of them generated
// in one pass, matching `wire ./...`'s discovery model instead of making
// the caller script a loop over --dir themselves.
func runGenerate(cfg load.Config) error {
	return runGenerateWith(cfg, generateOptions{})
}

// runGenerateWith is runGenerate with generate's own flags.
func runGenerateWith(cfg load.Config, opts generateOptions) error {
	pipelines, err := buildPipelines(cfg)
	if err != nil {
		return err
	}

	// Every injector's variant overlap is checked before any of them is
	// written. Resolution failures stay per-injector — generating the
	// injectors that are fine and reporting the ones that are not is
	// long-standing behaviour — but an overlap is a property of the
	// *output layout*, and discovering one halfway through would leave a
	// tree where some injectors had gained a variant and others had not,
	// from a command that exited non-zero.
	var errs []error
	for _, p := range pipelines {
		dir := filepath.Dir(p.spec.Pos.Filename)
		if err := checkVariantOverlap(dir, variantFileName(p.spec.Variant, false), p.spec.GeneratedConstraint); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.spec.InjectorPkg.PkgPath, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	// Resolution runs for every injector before anything is written, for
	// the same reason the overlap check does: whether a shared config's
	// companion loader takes the file map is a property of *all* the
	// injectors that use it (see checkConfigAgreement), and discovering a
	// disagreement halfway through would leave companions rewritten for
	// some injectors and stale for others.
	resolveds, resolveErrs, agreementErr := resolveAll(pipelines)
	errs = append(errs, resolveErrs...)
	if agreementErr != nil {
		return errors.Join(append(errs, agreementErr)...)
	}

	// And every graph an injector writes a file for — the NewTestApp graph
	// its Overrides describe included — is resolved and emitted before any
	// of that injector's files is written. The override variant used to be
	// resolved after servo_gen.go was already on disk, so a broken Override
	// left a rewritten production file beside a test variant that was
	// missing or stale.
	var outputs []*injectorOutput
	for i, p := range pipelines {
		if resolveds[i] == nil {
			continue // resolution already failed and is already in errs
		}
		// Notes never stop generation: a capability-named method servo
		// will never call, or a Close no Stop bridges to. They go to stderr
		// with the rest of generate's commentary, so a clean run on a
		// graph with none still prints nothing.
		for _, n := range resolveds[i].Notes {
			fmt.Fprintln(os.Stderr, n.String())
		}
		out, err := planOne(p, resolveds[i])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.spec.InjectorPkg.PkgPath, err))
			continue
		}
		outputs = append(outputs, out)
	}
	orphans, err := orphanedCompanions(pipelines)
	if err != nil {
		errs = append(errs, err)
	}

	w := newFileWriter(os.Stderr, opts.verbose)
	written := map[string][]byte{}
	var pkgPaths []string
	for _, out := range outputs {
		if err := w.apply(out); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", out.injector, err))
			continue
		}
		maps.Copy(written, out.files)
		pkgPaths = append(pkgPaths, out.pkgs...)
		if out.empty {
			// `servo init` scaffolds exactly this, and it generates and
			// builds cleanly into a program that exits the moment it starts.
			w.logf("note: %s declares no servo.Root and no servo.HTTP(), so its App constructs nothing and Run returns at once — add a servo.Root[T]() for each component the program runs", out.injector)
		}
	}
	for _, path := range orphans {
		if err := w.remove(path, "its package no longer declares a //servo:config type"); err != nil {
			errs = append(errs, err)
		}
	}
	if err := verifyWritten(cfg, written, pkgPaths, opts.invoked); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// verifyWritten type-checks what generate just wrote, so a generated file
// the compiler would reject fails `servo generate` itself instead of the
// next `go build`. It runs after the write, deliberately: the broken
// output is what a bug report needs, and the `go mod tidy` a first import
// of a config decoder calls for needs the import to be on disk — which is
// also why a package go list cannot describe is reported as unchecked
// rather than failed (see load.Verify).
func verifyWritten(cfg load.Config, files map[string][]byte, pkgPaths []string, invoked invocation) error {
	if len(files) == 0 {
		return nil
	}
	v, err := load.Verify(cfg, files, pkgPaths)
	if err != nil {
		return fmt.Errorf("servo: verifying the generated code: %w", err)
	}
	warnUnchecked(os.Stderr, "servo generate", v.Unchecked, invoked.command("generate", cfg.Build.TagList()))
	if len(v.Problems) == 0 {
		return nil
	}
	return compileFailure("servo: the generated code does not compile — it was written anyway, so it can be inspected", v.Problems, invoked.command("generate", cfg.Build.TagList()))
}

// compileFailure is the report for generated code the compiler rejects.
// The likely causes come before any mention of a bug, because they are
// likely: a mock not regenerated since its interface changed is an
// everyday one, and the report used to open by calling every such failure
// "a bug in servo rather than in your code", which sent people to the
// issue tracker with their own stale mocks.
func compileFailure(headline string, problems []string, rerun string) error {
	return fmt.Errorf(`%s:

  %s

The usual cause is in the code it was generated from, not in servo:
  - a servo.Bind or servo.Override type that no longer implements its interface,
    typically a mock not regenerated since the interface changed;
  - a provider the injector's package cannot refer to: one under another tree's
    internal/ directory, one whose package imports the injector's package, or one
    taking or returning another package's unexported type;
  - a declaration in the injector's package with a name the generated code declares too.
Fix the cause, then run %s again. If none of these explains it, it is a bug in
servo: please report it at https://github.com/okian/servo/issues with the lines above`,
		headline, strings.Join(problems, "\n  "), rerun)
}

// warnUnchecked names each package whose generated code could not be
// compiled, with the go command's reason and what to run. It used to be
// skipped in silence, which is how generate came to exit 0 on a file
// `go build` then rejected for a missing go.sum line.
//
// A warning that cannot be written is dropped: failing the command over
// it would turn a run that did its work into one that reports failure.
func warnUnchecked(w io.Writer, command string, unchecked []load.Unchecked, rerun string) {
	for _, u := range unchecked {
		_, _ = fmt.Fprintf(w, "%s: warning: could not compile the generated code in %s: %s\n", command, u.PkgPath, u.Reason)
	}
	if len(unchecked) > 0 {
		_, _ = fmt.Fprintf(w, "%s: run `go mod tidy`, then %s again to check that it compiles\n", command, rerun)
	}
}

// injectorOutput is one injector's generation, resolved and emitted but
// not yet written.
type injectorOutput struct {
	// injector is the injector package's import path, for messages.
	injector string
	// files is every file the injector's generation writes, by path: its
	// own file, the override variant when there is one, and the companion
	// config loaders of both graphs.
	files map[string][]byte
	// pkgs is the import path of every package a file lands in, for the
	// verification that follows the write.
	pkgs []string
	// leftover is an override variant an earlier generation wrote, which
	// goes now that the spec declares no servo.Override; "" for none.
	leftover string
	// empty is true for a graph with nothing to construct or serve.
	empty bool
}

// planOne resolves and emits everything one injector's generation writes,
// without writing any of it.
func planOne(p *pipeline, resolved *resolve.Resolved) (*injectorOutput, error) {
	out, err := emit.Emit(resolved, p.spec, false)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(p.spec.Pos.Filename)
	name := variantFileName(p.spec.Variant, false)
	// runGenerate checks every injector's overlap before planning any, so
	// this only matters to a caller that skips that pass — and stops it
	// from writing a pair of files that cannot compile together.
	if err := checkVariantOverlap(dir, name, p.spec.GeneratedConstraint); err != nil {
		return nil, err
	}
	o := &injectorOutput{
		injector: p.spec.InjectorPkg.PkgPath,
		files:    map[string][]byte{filepath.Join(dir, name): out},
		pkgs:     []string{p.spec.InjectorPkg.PkgPath},
		empty:    len(p.spec.Roots) == 0 && p.spec.HTTP == nil,
	}
	// Companion loaders land beside their config types, not the injector.
	// Two injectors sharing a config write identical bytes (agreement was
	// checked before any write), so the second write finds nothing to do.
	if err := o.addCompanions(resolved, p.spec.ConfigFile != nil); err != nil {
		return nil, err
	}

	testPath := filepath.Join(dir, variantFileName(p.spec.Variant, true))
	if len(p.spec.Overrides) == 0 {
		// Only a file servo itself wrote is touched, which its header is
		// the evidence of.
		if _, ok := servoGeneratedConstraint(testPath); ok {
			o.leftover = testPath
		}
		return o, nil
	}
	testResolved, err := p.resolve(p.spec.Overrides)
	if err != nil {
		return nil, inTestGraph(p, err)
	}
	testOut, err := emit.Emit(testResolved, p.spec, true)
	if err != nil {
		return nil, inTestGraph(p, err)
	}
	o.files[testPath] = testOut
	// An Override can swap in a constructor that consumes a config the
	// production graph never touches — NewTestApp still calls its loader,
	// so its companion has to exist. Ones both graphs use emit identically.
	if err := o.addCompanions(testResolved, p.spec.ConfigFile != nil); err != nil {
		return nil, err
	}
	return o, nil
}

// addCompanions adds the companion config loaders resolved's graph needs.
func (o *injectorOutput) addCompanions(resolved *resolve.Resolved, withFile bool) error {
	companions, err := companionFiles(resolved, withFile)
	if err != nil {
		return err
	}
	maps.Copy(o.files, companions)
	for _, n := range resolved.Configs {
		o.pkgs = append(o.pkgs, n.Config.PkgPath)
	}
	return nil
}

// inTestGraph says that a failure is in the NewTestApp graph, the one the
// spec's servo.Override declarations describe. Nothing else in the
// diagnostic says so, and its needed-by chain reads exactly like a
// production one — while the production graph resolved fine.
func inTestGraph(p *pipeline, err error) error {
	at := make([]string, len(p.spec.Overrides))
	for i, o := range p.spec.Overrides {
		at[i] = o.Pos.String()
	}
	return fmt.Errorf("in the NewTestApp graph (servo.Override at %s): %w", strings.Join(at, ", "), err)
}

// fileWriter writes a generation's files and says what it did.
type fileWriter struct {
	log     io.Writer
	verbose bool
	// done is every path this run has already written or found current: a
	// companion two injectors share is handled, and listed, once.
	done map[string]bool
}

func newFileWriter(log io.Writer, verbose bool) *fileWriter {
	return &fileWriter{log: log, verbose: verbose, done: map[string]bool{}}
}

// apply writes one injector's files, in path order, then removes the
// override variant it no longer has.
func (w *fileWriter) apply(o *injectorOutput) error {
	for _, path := range slices.Sorted(maps.Keys(o.files)) {
		if err := w.write(path, o.files[path]); err != nil {
			return err
		}
	}
	if o.leftover != "" {
		// Nothing would ever regenerate it, yet go test keeps compiling it,
		// so it drifts from the moment the last Override went — the same
		// orphan doctor reports for a tagged variant whose spec was deleted.
		return w.remove(o.leftover, "the spec no longer declares servo.Override")
	}
	return nil
}

// write puts data at path unless the file already holds exactly those
// bytes. Rewriting identical bytes still moves the file's mtime, and a
// watcher running `servo generate && go build` on every change rebuilds on
// that, forever: air rebuilt the tutorial eighteen times in twenty-five
// seconds with nothing edited.
func (w *fileWriter) write(path string, data []byte) error {
	if w.done[path] {
		return nil
	}
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		w.done[path] = true
		w.verbosef("unchanged %s", path)
		return nil
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		return err
	}
	w.done[path] = true
	w.verbosef("wrote %s", path)
	return nil
}

// remove deletes a generated file nothing produces any more. It is always
// reported, -v or not: deleting a file nobody named should never be
// silent.
func (w *fileWriter) remove(path, why string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	w.logf("removed %s — %s", path, why)
	return nil
}

func (w *fileWriter) verbosef(format string, args ...any) {
	if w.verbose {
		w.logf(format, args...)
	}
}

// logf writes one line of the run's report. A line that cannot be written
// is dropped: the files are already on disk, and failing the command over
// its own report would say the work was not done when it was.
func (w *fileWriter) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.log, "servo generate: "+format+"\n", args...)
}

// orphanedCompanions finds the config loaders servo generated for a
// //servo:config type that has since gone — moved to another package, or
// its directive deleted. Nothing regenerates such a file, yet it still
// compiles into every build and fails it, naming a type that is no longer
// there, while check compared only the companions a graph still uses and
// called the module current.
//
// The search walks the directory tree of every module the pipelines'
// scope covers — the modules whose //servo:config declarations this load
// scanned — rather than the load itself, because the commonest orphan is
// invisible to the load: when a config type moves out of a package that
// held nothing else, the companion is the directory's only Go file, and
// its !servoinject constraint hides it from the servoinject load, so go
// list does not report that directory at all.
//
// A companion is an orphan when its directory holds no other non-test Go
// file, so nothing there can declare the type it loads, or when the load
// saw that directory's package and found no //servo:config type in it. A
// directory the load did not see but which holds other Go files — a
// package gated out of this build configuration — is left alone: nothing
// this run read can say whether its type still exists.
//
// Only files carrying servo's own generated-code header are candidates.
func orphanedCompanions(pipelines []*pipeline) ([]string, error) {
	if len(pipelines) == 0 || pipelines[0].loaded == nil {
		return nil, nil
	}
	loaded, configs := pipelines[0].loaded, pipelines[0].configs

	roots := map[string]bool{}
	for _, p := range pipelines {
		for pkgPath := range p.scope {
			if pkg := loaded.ByPath[pkgPath]; pkg != nil && pkg.Module != nil && pkg.Module.Dir != "" {
				roots[filepath.Clean(pkg.Module.Dir)] = true
			}
		}
	}
	declares := map[string]bool{}
	for _, decl := range configs {
		declares[filepath.Clean(decl.Dir)] = true
	}
	seen := map[string]bool{}
	for _, pkg := range loaded.All {
		if len(pkg.GoFiles) > 0 {
			seen[filepath.Dir(pkg.GoFiles[0])] = true
		}
	}

	var orphans []string
	for _, root := range slices.Sorted(maps.Keys(roots)) {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				return nil // an unreadable corner of the tree is not this search's business
			}
			if d.IsDir() {
				if path != root && skipWalkDir(path, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() != emit.ConfigLoaderFileName {
				return nil
			}
			if _, ok := servoGeneratedConstraint(path); !ok {
				return nil
			}
			dir := filepath.Dir(path)
			if declares[dir] {
				return nil
			}
			if seen[dir] || !holdsOtherGoFiles(dir) {
				orphans = append(orphans, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("servo: looking for left-over config loaders: %w", err)
		}
	}
	return orphans, nil
}

// skipWalkDir reports whether the tree walk skips a directory, by the
// rules `./...` follows: dot and underscore directories, testdata and
// vendor are not packages of the module, and a directory with its own
// go.mod is another module.
func skipWalkDir(path, name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// holdsOtherGoFiles reports whether dir has a Go file besides the
// companion loader and test files — anything that could declare a type.
func holdsOtherGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true // cannot tell, so it is not an orphan
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && name != emit.ConfigLoaderFileName {
			return true
		}
	}
	return false
}

// writeFileAtomic writes data to path by writing a temp file in the same
// directory and renaming it into place, so a reader (an editor, a build
// racing a concurrent `servo generate`) never observes a partially-written
// file, and a process killed mid-write leaves the previous, complete
// version untouched rather than a truncated one. The temp file must live
// in the same directory as path: os.Rename is only atomic within a single
// filesystem, and a directory is the natural boundary for that.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// No-op once the rename below succeeds; cleans up on any earlier error.
	// The error is dropped deliberately — there is nothing useful to do when
	// removing a temp file fails, and any real error is already on its way up.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() // the write error is the one worth reporting
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
