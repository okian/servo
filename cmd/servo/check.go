package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/okian/servo/v3/internal/emit"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
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

	// Resolved once per injector, up front, so the cross-injector config
	// agreement check runs against all of them — a disagreement is a
	// property of the module, and CI should say so even when every
	// individual injector's own file happens to be fresh. check writes
	// nothing, so unlike generate it reports the disagreement and keeps
	// checking rather than stopping.
	resolveds, errs, agreementErr := resolveAll(pipelines)
	if agreementErr != nil {
		errs = append(errs, agreementErr)
	}

	for i, p := range pipelines {
		if resolveds[i] == nil {
			continue // resolution already failed and is already in errs
		}
		if err := checkOne(p, resolveds[i]); err != nil {
			errs = append(errs, err)
		}
		if err := checkTestVariant(p); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// checkTestVariant is checkOne for the override variant, resolved here
// rather than handed in since no cross-injector check needs it. A spec
// with no Override must have no servo-written test variant beside it:
// generate removes one it finds, and check refuses one that lingered —
// it still compiles under go test, and nothing regenerates it.
func checkTestVariant(p *pipeline) error {
	dir := filepath.Dir(p.spec.Pos.Filename)
	path := filepath.Join(dir, variantFileName(p.spec.Variant, true))
	if len(p.spec.Overrides) == 0 {
		if _, ok := servoGeneratedConstraint(path); ok {
			return fmt.Errorf("servo check: %s is left over from a spec that no longer declares servo.Override — run %s, which removes it", path, regenerateCommand(p.spec.Variant))
		}
		return nil
	}
	resolved, err := p.resolve(p.spec.Overrides)
	if err != nil {
		return err
	}
	fresh, err := emit.Emit(resolved, p.spec, true)
	if err != nil {
		return err
	}
	if err := compareGenerated(path, fresh, p.spec.Variant); err != nil {
		return err
	}
	companions, err := companionFiles(resolved, p.spec.ConfigFile != nil)
	if err != nil {
		return err
	}
	return compareCompanions(companions, p.spec.Variant)
}

// checkPipeline is resolve + checkOne in one call, for callers (doctor)
// that check a single injector and have no cross-injector concerns.
func checkPipeline(p *pipeline) error {
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	return checkOne(p, resolved)
}

func checkOne(p *pipeline, resolved *resolve.Resolved) error {
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

	if err := compareGenerated(filepath.Join(dir, name), fresh, p.spec.Variant); err != nil {
		return err
	}

	// Companion config loaders are as much this graph's output as the
	// injector file is, and drift the same way: a renamed tag regenerated
	// on one machine and committed without the companion.
	companions, err := companionFiles(resolved, p.spec.ConfigFile != nil)
	if err != nil {
		return err
	}
	return compareCompanions(companions, p.spec.Variant)
}

// compareGenerated reports a generated file that is missing or differs
// from fresh, naming the command that would produce it.
func compareGenerated(path string, fresh []byte, tags []string) error {
	committed, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("servo check: %s does not exist — run %s", path, regenerateCommand(tags))
		}
		return err
	}
	if string(committed) != string(fresh) {
		return fmt.Errorf("servo check: %s is stale — run %s\n%s\n%s", path, regenerateCommand(tags), unifiedDiff(string(committed), string(fresh), path), versionSkewNote())
	}
	return nil
}

// compareCompanions is compareGenerated over every companion loader, in
// path order so a report is stable.
func compareCompanions(companions map[string][]byte, tags []string) error {
	paths := make([]string, 0, len(companions))
	for path := range companions {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := compareGenerated(path, companions[path], tags); err != nil {
			return err
		}
	}
	return nil
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
