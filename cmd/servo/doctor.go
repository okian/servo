package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/okian/servo/v2/internal/load"
)

// runDoctor diagnoses setup problems before `go generate` is ever run,
// across every injector found under dir — the same scope generate/check
// process, not just one: can the spec(s) be found at all (which, via
// FindSpecs, already enforces the build-tag requirement), is the module
// otherwise free of build errors outside the injector(s), does each
// generated file exist, is it fresh, and — best-effort, never fatal — is
// it tracked by VCS.
func runDoctor(dir string) error {
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

	loaded, caps, err := loadModule(dir)
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

	multi := len(specs) > 1
	for _, spec := range specs {
		if multi {
			fmt.Printf("  -- %s --\n", spec.InjectorPkg.PkgPath)
		}
		report(true, "spec file found at %s, correctly gated by the servoinject build tag", spec.Pos)

		outPath := filepath.Join(filepath.Dir(spec.Pos.Filename), generatedFileName)
		if _, statErr := os.Stat(outPath); statErr != nil {
			report(false, "generated file missing: %s (run `servo generate`)", outPath)
			continue
		}
		report(true, "generated file present: %s", outPath)

		if err := checkOne(pipelineFor(loaded, caps, spec)); err != nil {
			report(false, "generated file is stale (run `servo generate`): %v", err)
		} else {
			report(true, "generated file matches a fresh generation")
		}
		if trackedByGit(dir, outPath) {
			report(true, "generated file is tracked by git")
		} else {
			fmt.Printf("  [WARN] generated file may not be committed — it should be, so builds are reproducible without running `servo generate` first\n")
		}
	}

	if problems {
		return fmt.Errorf("servo doctor: problems found")
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
