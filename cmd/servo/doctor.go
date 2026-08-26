package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/okian/servo/v2/internal/load"
)

// runDoctor diagnoses setup problems before `go generate` is ever run: can
// the spec be found at all (which, via FindSpec, already enforces the
// build-tag requirement), does the generated file exist, is it fresh, and
// — best-effort, never fatal — is it tracked by VCS.
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

	loaded, err := load.Load(load.Config{Dir: dir})
	if err != nil {
		report(false, "load module: %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}
	spec, err := load.FindSpec(loaded)
	if err != nil {
		report(false, "find spec file: %v", err)
		return fmt.Errorf("servo doctor: problems found")
	}
	report(true, "spec file found at %s, correctly gated by the servoinject build tag", spec.Pos)

	outPath := filepath.Join(filepath.Dir(spec.Pos.Filename), generatedFileName)
	if _, err := os.Stat(outPath); err != nil {
		report(false, "generated file missing: %s (run `servo generate`)", outPath)
	} else {
		report(true, "generated file present: %s", outPath)
		if err := runCheck(dir); err != nil {
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
