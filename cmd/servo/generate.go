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
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
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
	return os.WriteFile(testOutPath, testOut, 0o644)
}
