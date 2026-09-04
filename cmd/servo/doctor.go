package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/okian/servo/v3/internal/load"
)

// runDoctor diagnoses setup problems before `go generate` is ever run,
// across every injector found under dir — the same scope generate/check
// process, not just one: can the spec(s) be found at all (which, via
// FindSpecs, already enforces the build-tag requirement), is the module
// otherwise free of build errors outside the injector(s), does each
// generated file exist, is it fresh, and — best-effort, never fatal — is
// it tracked by VCS.
func runDoctor(cfg load.Config) error {
	fmt.Println("servo doctor:")
	problems := false
	report := func(ok bool, format string, args ...any) {
		status := "OK  "
		if !ok {
			status = "FAIL"
			problems = true
		}
		fmt.Printf("  [%s] %s\n", status, fmt.Sprintf(format, args...))
	}

	loaded, caps, configs, err := loadModule(cfg)
	if err != nil {
		report(false, "load module: %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}
	specs, err := load.FindSpecs(loaded)
	if err != nil {
		report(false, "find spec file(s): %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}

	injectorPaths := make([]string, len(specs))
	for i, s := range specs {
		injectorPaths[i] = s.InjectorPkg.PkgPath
	}
	if err := loaded.NonInjectorErrors(injectorPaths...); err != nil {
		report(false, "module has build errors outside the injector(s): %v", err)
	} else {
		report(true, "no build errors outside the injector(s)")
	}

	// An injector whose spec is gated out of this configuration gets no
	// generated file here, so its main.go's call to New has nothing behind
	// it. Nothing else reports this: generate and check are right to stay
	// silent (an injector deliberately excluded from a configuration is
	// legitimate), but the compiler\'s eventual "undefined: New" explains
	// none of it.
	for _, pkgPath := range loaded.InjectorsInOtherConfigurations(injectorPaths...) {
		report(false, "%s holds a spec file this configuration cannot see, so nothing generates its New — either give it a variant for these flags, or gate the package itself out of this build", pkgPath)
	}

	multi := len(specs) > 1
	for _, spec := range specs {
		if multi {
			fmt.Printf("  -- %s --\n", spec.InjectorPkg.PkgPath)
		}
		report(true, "spec file found at %s, correctly gated by the servoinject build tag", spec.Pos)

		outPath := filepath.Join(filepath.Dir(spec.Pos.Filename), variantFileName(spec.Variant, false))
		if _, statErr := os.Stat(outPath); statErr != nil {
			report(false, "generated file missing: %s (run %s)", outPath, regenerateCommand(spec.Variant))
			continue
		}
		report(true, "generated file present: %s", outPath)

		if err := checkPipeline(pipelineFor(loaded, caps, configs, spec)); err != nil {
			report(false, "generated file is stale (run %s): %v", regenerateCommand(spec.Variant), err)
		} else {
			report(true, "generated file matches a fresh generation")
		}
		if trackedByGit(cfg.Dir, outPath) {
			report(true, "generated file is tracked by git")
		} else {
			fmt.Printf("  [WARN] generated file may not be committed — it should be, so builds are reproducible without running `servo generate` first\n")
		}

		// Everything above concerns the one variant these flags select.
		// Without the inventory below, a project's other variants are
		// invisible to every servo command: a stale prod variant, or one
		// whose spec was deleted while its generated file stayed behind,
		// draws three green servo commands next to a red `go build`.
		if err := reportVariants(spec, variantFileName(spec.Variant, false), report); err != nil {
			report(false, "reading sibling generated files: %v", err)
		}
	}

	if problems {
		return fmt.Errorf("servo doctor: problems found")
	}
	return nil
}

// reportVariants inventories the other generated files sitting beside this
// spec: the ones no spec accounts for any more, and the ones this run did
// not verify because it was invoked with different flags.
func reportVariants(spec *load.Spec, ownName string, report func(bool, string, ...any)) error {
	dir := filepath.Dir(spec.Pos.Filename)
	variants, err := discoverVariants(dir, load.SpecConstraintsIn(spec.InjectorPkg))
	if err != nil {
		return err
	}

	var unverified []string
	for _, v := range variants {
		switch {
		case !v.owned:
			// Not a warning. The file still compiles into whichever build
			// satisfies its constraint, and nothing will ever regenerate
			// it, so it drifts silently from the moment its spec went away.
			report(false, "%s is generated from a spec that no longer exists — delete it, or restore the spec file it came from", v.name)
		case v.name == ownName || v.name == variantFileName(spec.Variant, true):
			// The variant this run is about; already reported above.
		default:
			unverified = append(unverified, fmt.Sprintf("%s (%s)", v.name, regenerateCommand(v.tags)))
		}
	}
	if len(unverified) > 0 {
		fmt.Printf("  [INFO] not checked by this run, being other variants: %s\n", strings.Join(unverified, ", "))
	}
	return nil
}

// trackedByGit is best-effort: any failure (git missing, not a repo, etc.)
// just means "can't tell", not "problem found".
func trackedByGit(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		rel = path
	}
	cmd := exec.Command("git", "-C", dir, "ls-files", "--error-unmatch", rel)
	return cmd.Run() == nil
}
