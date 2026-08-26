package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okian/servo/v2/internal/emit"
)

// generatedFileName is the emitted file's name, placed alongside the spec
// file it was generated from. generatedTestFileName is the servotest
// override variant — a _test.go file so it compiles only during `go test`,
// since NewTestApp/TestApp have no reason to exist in the real binary.
const (
	generatedFileName     = "servo_gen.go"
	generatedTestFileName = "servo_gen_test.go"
)

// runGenerate processes every injector found within dir's scope. A module
// with one injector behaves exactly as before; a module with several (a
// monorepo's cmd/api, cmd/worker, cmd/migrator) gets all of them generated
// in one pass, matching `wire ./...`'s discovery model instead of making
// the caller script a loop over --dir themselves.
func runGenerate(dir string) error {
	pipelines, err := buildPipelines(dir)
	if err != nil {
		return err
	}

	var errs []error
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
	outPath := filepath.Join(filepath.Dir(p.spec.Pos.Filename), generatedFileName)
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
	testOutPath := filepath.Join(filepath.Dir(p.spec.Pos.Filename), generatedTestFileName)
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
	defer os.Remove(tmpPath) // no-op once the rename below succeeds; cleans up on any earlier error

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
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
