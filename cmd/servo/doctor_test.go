package main

import (
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/load"
	"golang.org/x/tools/go/packages"
)

// TestRunDoctorAggregatesEveryInjector covers a multi-injector module
// where one injector is fully healthy (generated, fresh, git-tracked) and
// another is missing its generated file entirely: doctor previously used
// FindSpec (singular), which errors outright ("multiple injectors found
// — pass --dir") the moment more than one exists, making it unusable for
// a multi-injector module at all. It must now report on every injector
// found, the same scope generate/check already use, and still fail
// overall because of the one real problem.
func TestRunDoctorAggregatesEveryInjector(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/doctormulti\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "worker/worker.go", "package worker\n\ntype Consumer struct{}\n\nfunc New() *Consumer { return &Consumer{} }\n")
	mustWriteFile(t, dir, "cmd/apisvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/doctormulti/api"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*api.Server]()) }
`)
	mustWriteFile(t, dir, "cmd/workersvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/doctormulti/worker"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*worker.Consumer]()) }
`)
	runGoModTidy(t, dir)

	// Generate only apisvc, leaving workersvc's servo_gen.go missing.
	if err := runGenerate(cfg(filepath.Join(dir, "cmd", "apisvc"))); err != nil {
		t.Fatalf("runGenerate(cfg(apisvc)): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "apisvc", generatedFileName)); err != nil {
		t.Fatalf("expected apisvc's servo_gen.go to exist: %v", err)
	}

	var doctorErr error
	out := captureStdout(t, func() { doctorErr = runDoctor(cfg(dir)) })
	if doctorErr == nil {
		t.Fatal("expected an overall failure: workersvc has no generated file")
	}
	if !strings.Contains(out, "apisvc") || !strings.Contains(out, "workersvc") {
		t.Errorf("expected both injectors named in the report, got:\n%s", out)
	}
	if !strings.Contains(out, "generated file present") {
		t.Errorf("expected apisvc reported healthy, got:\n%s", out)
	}
	if !strings.Contains(out, "generated file missing") {
		t.Errorf("expected workersvc reported missing its generated file, got:\n%s", out)
	}
}

// TestRunDoctorFailsOnNonInjectorBuildErrors covers doctor's own
// NonInjectorErrors check: a real compile error in a sibling package,
// unrelated to the injector itself, must be reported — generate/check
// already gate on this before ever writing a file, so doctor reporting
// "all green" here would be a false all-clear for something generate
// would immediately fail on.
func TestRunDoctorFailsOnNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorbrokensibling", true, "")
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected runDoctor to fail on a build error outside the injector")
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "build errors outside the injector") {
		t.Errorf("expected a build-errors FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenModuleDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/nolib\n\ngo 1.23\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil || !strings.Contains(err.Error(), "problems found") {
		t.Fatalf("got err=%v, want 'problems found'", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "load module") {
		t.Errorf("expected a 'load module' FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenSpecMissing(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/nospec\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nimport _ \"github.com/okian/servo/v3/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil || !strings.Contains(err.Error(), "problems found") {
		t.Fatalf("got err=%v, want 'problems found'", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "find spec file") {
		t.Errorf("expected a 'find spec file' FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenGeneratedFileMissing(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctormissing", true, "")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected an error when the generated file is missing")
	}
	if !strings.Contains(out, "generated file missing") {
		t.Errorf("expected a 'generated file missing' line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenGeneratedFileStale(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorstale", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	if err := os.WriteFile(genPath, []byte("//go:build !servoinject\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected an error when the generated file is stale")
	}
	if !strings.Contains(out, "generated file is stale") {
		t.Errorf("expected a 'generated file is stale' line, got:\n%s", out)
	}
}

func TestRunDoctorWarnsWhenNotTrackedByGit(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctoruntracked", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err != nil {
		t.Fatalf("runDoctor should succeed (untracked is a warning, not a failure): %v", err)
	}
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "may not be committed") {
		t.Errorf("expected an untracked-by-git WARN line, got:\n%s", out)
	}
}

func TestRunDoctorCleanWhenTrackedByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := writeAppModule(t, "example.com/doctorclean", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	// Every invocation is isolated from the developer's own git
	// configuration. core.hooksPath is frequently set globally, and those
	// hooks would otherwise run inside this throwaway repository and fail
	// the commit for reasons that have nothing to do with servo (a
	// protected-branch guard, a formatter, a spell checker). Signing is
	// disabled for the same reason: a global commit.gpgsign would block a
	// commit no interactive agent is present to authorize. Both make the
	// test pass in CI and fail on a contributor's machine.
	isolated := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgsign=false",
	}
	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append(append([]string{}, isolated...), args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err != nil {
		t.Fatalf("runDoctor on a fully clean, committed tree: %v", err)
	}
	if !strings.Contains(out, "generated file is tracked by git") {
		t.Errorf("expected a tracked-by-git OK line, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("expected no FAIL lines, got:\n%s", out)
	}
}

func TestTrackedByGitOnNonRepo(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "servo_gen.go", "package main\n")
	if trackedByGit(dir, filepath.Join(dir, "servo_gen.go")) {
		t.Error("trackedByGit on a directory with no .git should be false")
	}
}

// TestTrackedByGitFallsBackWhenPathsCannotBeMadeRelative covers the
// filepath.Rel error branch: a relative dir paired with an absolute path
// (dir="." is the CLI's own default, and go/packages always reports
// absolute file positions) can't be resolved to a relative path by lexical
// analysis alone, so trackedByGit must fall back to using path as-is
// rather than propagating the error or panicking.
func TestTrackedByGitFallsBackWhenPathsCannotBeMadeRelative(t *testing.T) {
	if trackedByGit(".", "/absolute/path/servo_gen.go") {
		t.Error("trackedByGit with an unrelatable dir/path pair should be false, not panic or true")
	}
}

// TestReportVariantsSurfacesAnUnreadableInventory: every other failure in
// doctor is reported and the run continues, so it would be tempting to let
// this one return nothing too. It must not. The variant inventory is the
// only thing in servo that ever notices an orphaned or unverified
// generated file, and "no other variants" is precisely what it prints for
// a healthy single-variant project — so an inventory that could not be
// read has to be distinguishable from an empty one, and nothing may be
// reported off the back of it.
func TestReportVariantsSurfacesAnUnreadableInventory(t *testing.T) {
	// An unmatched '[' in the injector's own directory name is legal on
	// every filesystem servo runs on, and makes the inventory's glob
	// pattern invalid.
	dir := filepath.Join(t.TempDir(), "proj[1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := &load.Spec{
		Pos:         token.Position{Filename: filepath.Join(dir, "spec.go")},
		InjectorPkg: &packages.Package{},
	}

	var reported []string
	err := reportVariants(spec, generatedFileName, func(_ bool, format string, args ...any) {
		reported = append(reported, fmt.Sprintf(format, args...))
	})
	if err == nil {
		t.Fatal("reportVariants = nil, want the unreadable inventory reported")
	}
	if len(reported) != 0 {
		t.Errorf("reportVariants drew conclusions from an inventory it could not read: %v", reported)
	}
}
