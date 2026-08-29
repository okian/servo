package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/load"
)

// runCheck verifies every injector found within dir's scope matches a fresh
// generation, reporting every stale one — not just the first — so a CI job
// checking the whole module surfaces every drifted injector in one run, the
// same way `wire ./... && git diff --exit-code` would. Never rewrites
// anything itself (that's `generate`'s job).
func runCheck(cfg load.Config) error {
	pipelines, err := buildPipelines(cfg)
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

	dir := filepath.Dir(p.spec.Pos.Filename)
	name := variantFileName(p.spec.Variant, false)
	// Reported here too, not only by generate: an overlap committed
	// before this check existed, or produced by an older servo, is
	// exactly the thing CI should refuse to let past.
	if err := checkVariantOverlap(dir, name, p.spec.GeneratedConstraint); err != nil {
		return err
	}

	outPath := filepath.Join(dir, name)
	committed, err := os.ReadFile(outPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("servo check: %s does not exist — run %s", outPath, regenerateCommand(p.spec.Variant))
		}
		return err
	}

	if string(committed) == string(fresh) {
		return nil
	}
	return fmt.Errorf("servo check: %s is stale — run %s\n%s\n%s", outPath, regenerateCommand(p.spec.Variant), unifiedDiff(string(committed), string(fresh), outPath), versionSkewNote())
}

// versionSkewNote is appended to every stale report because the message
// above has exactly one other cause, and no way to tell them apart from
// the diff.
//
// A change to generated code's internal shape is explicitly not a breaking
// change — consumers regenerate — so two machines on two servo versions
// produce a real difference in a file neither of them edited. Without this
// note the loop is: CI says stale, the developer runs the command it names
// with their own binary, pushes, and CI says stale again.
func versionSkewNote() string {
	return "note: this is servo " + version() + ". If regenerating does not settle it, the machine that\n" +
		"      committed the file was running a different version — compare `servo version`, and\n" +
		"      pin one for everybody with `go get -tool " + toolPath + "`."
}

const toolPath = "github.com/okian/servo/v3/cmd/servo"
