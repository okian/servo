package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/load"
)

// runGenerate processes every injector found within dir's scope. A module
// with one injector behaves exactly as before; a module with several (a
// monorepo's cmd/api, cmd/worker, cmd/migrator) gets all of them generated
// in one pass, matching `wire ./...`'s discovery model instead of making
// the caller script a loop over --dir themselves.
func runGenerate(cfg load.Config) error {
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

	for _, p := range pipelines {
		if err := generateOne(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.spec.InjectorPkg.PkgPath, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func generateOne(p *pipeline) error {
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	out, err := emit.Emit(resolved, p.spec, false)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.spec.Pos.Filename)
	name := variantFileName(p.spec.Variant, false)
	if err := checkVariantOverlap(dir, name, p.spec.GeneratedConstraint); err != nil {
		return err
	}
	outPath := filepath.Join(dir, name)
	if err := writeFileAtomic(outPath, out, 0o644); err != nil {
		return err
	}

	if len(p.spec.Overrides) == 0 {
		return nil
	}
	testResolved, err := p.resolve(p.spec.Overrides)
	if err != nil {
		return err
	}
	testOut, err := emit.Emit(testResolved, p.spec, true)
	if err != nil {
		return err
	}
	testOutPath := filepath.Join(dir, variantFileName(p.spec.Variant, true))
	return writeFileAtomic(testOutPath, testOut, 0o644)
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
