package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okian/servo/v3/internal/emit"
)

// runCheck verifies every injector found within dir's scope matches a fresh
// generation, reporting every stale one — not just the first — so a CI job
// checking the whole module surfaces every drifted injector in one run, the
// same way `wire ./... && git diff --exit-code` would. Never rewrites
// anything itself (that's `generate`'s job).
func runCheck(dir string) error {
	pipelines, err := buildPipelines(dir)
	if err != nil {
		return err
	}

	var errs []error
	for _, p := range pipelines {
		if err := checkOne(p); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func checkOne(p *pipeline) error {
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	fresh, err := emit.Emit(resolved, p.spec, false)
	if err != nil {
		return err
	}

	outPath := filepath.Join(filepath.Dir(p.spec.Pos.Filename), generatedFileName)
	committed, err := os.ReadFile(outPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("servo check: %s does not exist — run `servo generate`", outPath)
		}
		return err
	}

	if string(committed) == string(fresh) {
		return nil
	}
	return fmt.Errorf("servo check: %s is stale — run `servo generate`\n%s", outPath, unifiedDiff(string(committed), string(fresh), outPath))
}
