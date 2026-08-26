package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoctorFailsWhenModuleDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/nolib\n\ngo 1.23\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	var err error
	out := captureStdout(t, func() { err = runDoctor(dir) })
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
	mustWriteFile(t, dir, "go.mod", "module example.com/nospec\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nimport _ \"github.com/okian/servo/v2/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	var err error
	out := captureStdout(t, func() { err = runDoctor(dir) })
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
	out := captureStdout(t, func() { err = runDoctor(dir) })
	if err == nil {
		t.Fatal("expected an error when the generated file is missing")
	}
	if !strings.Contains(out, "generated file missing") {
		t.Errorf("expected a 'generated file missing' line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenGeneratedFileStale(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorstale", true, "")
	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	if err := os.WriteFile(genPath, []byte("//go:build !servoinject\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(dir) })
	if err == nil {
		t.Fatal("expected an error when the generated file is stale")
	}
	if !strings.Contains(out, "generated file is stale") {
		t.Errorf("expected a 'generated file is stale' line, got:\n%s", out)
	}
}

func TestRunDoctorWarnsWhenNotTrackedByGit(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctoruntracked", true, "")
	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(dir) })
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
	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(dir) })
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
