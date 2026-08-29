package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInitScaffoldsSpecFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package worker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runInit(dir, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(dir, "servo_spec.go"))
	if err != nil {
		t.Fatalf("reading scaffolded spec: %v", err)
	}
	content := string(out)
	if !strings.Contains(content, "//go:build servoinject") {
		t.Errorf("scaffold missing build tag:\n%s", content)
	}
	if !strings.Contains(content, "package worker") {
		t.Errorf("scaffold should detect the package name from the existing main.go, got:\n%s", content)
	}
	if !strings.Contains(content, "//go:generate go run github.com/okian/servo/v3/cmd/servo generate") {
		t.Errorf("scaffold missing go:generate directive:\n%s", content)
	}
}

func TestRunInitFallsBackToMainPackage(t *testing.T) {
	dir := t.TempDir() // empty: no existing .go files to detect a package name from

	if err := runInit(dir, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "servo_spec.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "package main") {
		t.Errorf("expected fallback to 'package main', got:\n%s", out)
	}
}

func TestRunInitFailsWhenSpecAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "servo_spec.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runInit(dir, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got err=%v, want an 'already exists' error", err)
	}
}

func TestDetectPackageNameSkipsUnparseableFiles(t *testing.T) {
	dir := t.TempDir()
	// A non-.go file must be ignored outright, and a malformed .go file
	// must be skipped in favor of the next candidate rather than aborting.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("package ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("not valid go source {{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package detected\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := detectPackageName(dir); got != "detected" {
		t.Errorf("detectPackageName = %q, want %q", got, "detected")
	}
}

// TestDetectPackageNameSkipsDirectoryEntry covers the e.IsDir() half of
// detectPackageName's filter — TestDetectPackageNameSkipsUnparseableFiles
// only exercises the "not a .go file" half, and os.ReadDir returns entries
// sorted by name, so a subdirectory needs to sort before any valid .go
// file to actually reach this branch instead of the loop returning first.
func TestDetectPackageNameSkipsDirectoryEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "aaa_subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "zzz.go"), []byte("package detected\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := detectPackageName(dir); got != "detected" {
		t.Errorf("detectPackageName = %q, want %q (a subdirectory sorting first must be skipped)", got, "detected")
	}
}

func TestDetectPackageNameOnUnreadableDir(t *testing.T) {
	if got := detectPackageName(filepath.Join(t.TempDir(), "does-not-exist")); got != "main" {
		t.Errorf("detectPackageName on a missing dir = %q, want %q", got, "main")
	}
}

// TestRunInitFailsWhenMkdirAllBlockedByFile covers os.MkdirAll's own error
// branch: a path component that already exists as a regular file can never
// be descended into as a directory.
func TestRunInitFailsWhenMkdirAllBlockedByFile(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runInit(filepath.Join(blocker, "nested"), nil)
	if err == nil {
		t.Fatal("expected an error when dir's path is blocked by an existing file")
	}
}

// TestRunInitFailsWhenDirNotWritable covers os.WriteFile's error branch:
// MkdirAll on an already-existing directory is a no-op success regardless
// of its permissions, so a read-only (but already-existing) dir reaches
// WriteFile before failing there instead.
func TestRunInitFailsWhenDirNotWritable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }() // best-effort restore so t.TempDir cleanup can remove it

	if err := runInit(dir, nil); err == nil {
		t.Fatal("expected an error when the target directory is not writable")
	}
}

// TestRunInitScaffoldsAVariantSpec: the scaffold is where the one mistake
// the variant model allows is cheapest to prevent, so `servo init
// --tags=prod` must produce a spec already gated for prod, with a
// go:generate line that regenerates that variant rather than the default.
func TestRunInitScaffoldsAVariantSpec(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, []string{"prod"}); err != nil {
		t.Fatalf("init --tags=prod: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "servo_spec_prod.go"))
	if err != nil {
		t.Fatalf("expected servo_spec_prod.go beside the default spec: %v", err)
	}
	for _, want := range []string{
		"//go:build servoinject && prod",
		"servo generate --tags=prod",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffolded spec missing %q:\n%s", want, body)
		}
	}

	// The default name is untouched, so the two coexist.
	if _, err := os.Stat(filepath.Join(dir, "servo_spec.go")); !os.IsNotExist(err) {
		t.Errorf("init --tags=prod should not write servo_spec.go (stat err = %v)", err)
	}
}

// TestRunInitWarnsAboutAnUnexcludedSibling covers the step every user will
// forget: adding a prod variant beside a spec that does not exclude prod
// leaves two specs visible under --tags=prod, and `servo generate` would
// then refuse. Saying so at scaffold time is cheaper than saying so later.
func TestRunInitWarnsAboutAnUnexcludedSibling(t *testing.T) {
	dir := t.TempDir()
	if err := runInit(dir, nil); err != nil {
		t.Fatalf("init: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runInit(dir, []string{"prod"}); err != nil {
			t.Fatalf("init --tags=prod: %v", err)
		}
	})
	if !strings.Contains(out, "servo_spec.go is also visible with --tags=prod") {
		t.Errorf("expected a warning naming the unexcluded sibling, got:\n%s", out)
	}
	if !strings.Contains(out, "servoinject && !prod") {
		t.Errorf("expected the warning to give the constraint to narrow it to, got:\n%s", out)
	}
}

// TestRunInitRejectsAnUnusableVariantTag confirms the same validation the
// other commands apply reaches init, so a bad tag is caught before a spec
// file is written around it.
func TestRunInitRejectsAnUnusableVariantTag(t *testing.T) {
	err := run([]string{"init", "--dir", t.TempDir(), "--tags", "linux"})
	if err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
		t.Fatalf("init --tags=linux = %v, want the GOOS/GOARCH rejection", err)
	}
}
