package main

import (
	"os"
	"os/exec"
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
	// The directive is deliberately NOT in this file: go generate honours
	// build constraints, so one inside the servoinject file is invisible
	// to `go generate ./...`, which then exits 0 having done nothing.
	if strings.Contains(content, "go:generate") {
		t.Errorf("the go:generate directive must not live in the tagged spec file, where go generate cannot see it:\n%s", content)
	}

	gen, err := os.ReadFile(filepath.Join(dir, "servo_generate.go"))
	if err != nil {
		t.Fatalf("reading the scaffolded directive file: %v", err)
	}
	directive := string(gen)
	if strings.Contains(directive, "//go:build") {
		t.Errorf("the directive file must carry no build constraint at all:\n%s", directive)
	}
	for _, want := range []string{"package worker", "//go:generate go tool servo generate"} {
		if !strings.Contains(directive, want) {
			t.Errorf("directive file missing %q:\n%s", want, directive)
		}
	}
}

// TestRunInitScaffoldedWorkflowIsReachableByGoGenerate is the end-to-end
// half: it builds the module the scaffold describes and confirms `go
// generate ./...` actually reaches the directive. The unit assertions
// above cannot catch a directive that is present, correct, and never run.
func TestRunInitScaffoldedWorkflowIsReachableByGoGenerate(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", `module example.com/initflow

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	mustWriteFile(t, dir, "pkg/pkg.go", `package pkg

type T struct{}

func New() *T { return &T{} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	appDir := filepath.Join(dir, "cmd", "app")
	if err := runInit(appDir, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	mustWriteFile(t, dir, "cmd/app/servo_spec.go", `//go:build servoinject

package main

import (
	"example.com/initflow/pkg"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*pkg.T](),
	)
}
`)
	runGoModTidy(t, dir)

	// The go command has to be able to find the tool the directive names,
	// which is the second half of the same failure: without it, `go run
	// <the generator's package>` fails on a missing go.sum entry, because a
	// consumer requires servo for the marker package alone.
	get := exec.Command("go", "get", "-tool", "github.com/okian/servo/v3/cmd/servo")
	get.Dir = dir
	if out, err := get.CombinedOutput(); err != nil {
		t.Skipf("go get -tool needs the module proxy or a warm cache; skipping: %v\n%s", err, out)
	}

	gen := exec.Command("go", "generate", "./...")
	gen.Dir = dir
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("go generate ./...: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(appDir, "servo_gen.go")); err != nil {
		t.Fatalf("`go generate ./...` produced no servo_gen.go — the scaffolded directive was not reached: %v", err)
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
// --tags=prod` must produce a spec already gated for prod, carrying a
// generate directive that rebuilds that variant rather than the default.
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
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffolded spec missing %q:\n%s", want, body)
		}
	}

	gen, err := os.ReadFile(filepath.Join(dir, "servo_generate.go"))
	if err != nil {
		t.Fatalf("reading the scaffolded directive file: %v", err)
	}
	if !strings.Contains(string(gen), "servo generate --tags=prod") {
		t.Errorf("the directive should carry the variant's tags:\n%s", gen)
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

// TestRunInitStaysQuietWhenTheSiblingAlreadyExcludesTheNewTags is the case
// the warning must not fire on, and the reason it is worth a test of its
// own: a default spec already gated `servoinject && !prod` is invisible
// under --tags=prod, so the two variants can never compile together.
// Warning here anyway would train people to scroll past the message in the
// one case where it is telling them something true.
func TestRunInitStaysQuietWhenTheSiblingAlreadyExcludesTheNewTags(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "servo_spec.go", "//go:build servoinject && !prod\n\npackage main\n")

	out := captureStdout(t, func() {
		if err := runInit(dir, []string{"prod"}); err != nil {
			t.Fatalf("init --tags=prod: %v", err)
		}
	})
	if strings.Contains(out, "also visible with") {
		t.Errorf("a sibling that already excludes prod drew an overlap warning:\n%s", out)
	}
	// Proof the scan actually looked at the sibling rather than skipping
	// the directory outright: the run really did scaffold the variant.
	if !strings.Contains(out, "servo_spec_prod.go") {
		t.Errorf("init --tags=prod did not scaffold the variant at all:\n%s", out)
	}
}

// TestWarnAboutUnexcludedSiblingsSkipsAFileItCannotParse: the scan reads
// every .go file in the directory, including one being edited in another
// window. A file that does not parse has to be stepped over rather than
// end the scan, or a real overlapping sibling stays unreported behind it.
// os.ReadDir returns entries sorted by name, so the broken file has to
// sort first for this to prove anything.
func TestWarnAboutUnexcludedSiblingsSkipsAFileItCannotParse(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "aaa_broken.go", "not valid go source {{{")
	mustWriteFile(t, dir, "servo_spec.go", "//go:build servoinject\n\npackage main\n")

	out := captureStdout(t, func() {
		warnAboutUnexcludedSiblings(dir, filepath.Join(dir, "servo_spec_prod.go"), []string{"prod"})
	})
	if !strings.Contains(out, "servo_spec.go is also visible with --tags=prod") {
		t.Errorf("the scan stopped at the unparseable file instead of stepping over it, got:\n%s", out)
	}
}

// TestWarnAboutUnexcludedSiblingsOnAnUnreadableDirectory: the warning is
// advice printed after the spec file has already been written, so a
// directory it cannot read must leave the scaffold successful and silent
// rather than turn it into a failure.
func TestWarnAboutUnexcludedSiblingsOnAnUnreadableDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	out := captureStdout(t, func() {
		warnAboutUnexcludedSiblings(missing, filepath.Join(missing, "servo_spec_prod.go"), []string{"prod"})
	})
	if out != "" {
		t.Errorf("expected no output for a directory that cannot be read, got:\n%s", out)
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

// TestRunInitWritesTheDocumentedMain: a directory with no main function
// gets the main.go the docs describe, so `go generate ./... && go run .`
// works without the reader first having to discover how a generated App
// is run — and init says it wrote it, and how to run it.
func TestRunInitWritesTheDocumentedMain(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cmd", "app")

	out := captureStdout(t, func() {
		if err := runInit(dir, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}
	})
	body, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("init wrote no main.go into an empty package main: %v", err)
	}
	for _, want := range []string{"package main", `"github.com/okian/servo/v3/servo"`, "func main() { servo.Main(New) }"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("main.go missing %q:\n%s", want, body)
		}
	}
	for _, want := range []string{"servo init: wrote " + filepath.Join(dir, "main.go"), "go run " + dir} {
		if !strings.Contains(out, want) {
			t.Errorf("init output missing %q:\n%s", want, out)
		}
	}
}

// TestRunInitLeavesAnExistingMainAlone: a main function in any file of
// the directory means the program already has an entry point, and a
// second one would not compile — whatever the first file is called.
func TestRunInitLeavesAnExistingMainAlone(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "app.go", "package main\n\nfunc main() {}\n")

	out := captureStdout(t, func() {
		if err := runInit(dir, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Errorf("init wrote main.go beside an existing func main (stat err = %v)", err)
	}
	if strings.Contains(out, "go run") {
		t.Errorf("init suggested running a main it did not write:\n%s", out)
	}
}

// TestRunInitWritesNoMainOutsidePackageMain: a library package's
// injector is run by whoever imports it, so it gets no main of its own.
func TestRunInitWritesNoMainOutsidePackageMain(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "wiring.go", "package wiring\n")

	if err := runInit(dir, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Errorf("init wrote main.go into package wiring (stat err = %v)", err)
	}
}

func TestGoRunTarget(t *testing.T) {
	for in, want := range map[string]string{
		".":         ".",
		"cmd/app":   "./cmd/app",
		"./cmd/app": "./cmd/app",
		"../app":    "../app",
		"/abs/app":  "/abs/app",
	} {
		if got := goRunTarget(in); got != want {
			t.Errorf("goRunTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRunInitScaffoldBuilds is the end-to-end half of the main.go
// scaffold: init, a root, a generation, and the module must build. It
// needs servo.Main, so it waits for the runtime that declares it.
func TestRunInitScaffoldBuilds(t *testing.T) {
	if !servoDeclaresMain(t) {
		t.Skip("servo.Main is not declared in this checkout's servo package yet")
	}
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/initmain\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	mustWriteFile(t, dir, "pkg/pkg.go", "package pkg\n\ntype T struct{}\n\nfunc New() *T { return &T{} }\n")
	appDir := filepath.Join(dir, "cmd", "app")
	captureStdout(t, func() {
		if err := runInit(appDir, nil); err != nil {
			t.Fatalf("runInit: %v", err)
		}
	})
	mustWriteFile(t, dir, "cmd/app/servo_spec.go", `//go:build servoinject

package main

import (
	"example.com/initmain/pkg"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*pkg.T](),
	)
}
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	build := exec.Command("go", "build", "./...")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the scaffolded main.go does not build against the generated New: %v\n%s", err, out)
	}
}

// servoDeclaresMain reports whether this checkout's servo package
// declares Main, read from its source.
func servoDeclaresMain(t *testing.T) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "servo", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "\nfunc Main[") {
			return true
		}
	}
	return false
}

// TestDeclaresMainReadsOnlySourceFiles: a directory, a test file and a
// file that does not parse are passed over, and a directory that cannot
// be read declares nothing.
func TestDeclaresMainReadsOnlySourceFiles(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "sub/main.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "main_test.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "broken.go", "package main\n\nfunc main( {\n")
	mustWriteFile(t, dir, "notes.txt", "func main() {}\n")
	if declaresMain(dir) {
		t.Error("declaresMain found a main in a subdirectory, a test file, a broken file or a text file")
	}
	mustWriteFile(t, dir, "app.go", "package main\n\nfunc main() {}\n")
	if !declaresMain(dir) {
		t.Error("declaresMain missed app.go's func main")
	}
	if declaresMain(filepath.Join(dir, "missing")) {
		t.Error("declaresMain on a missing directory = true")
	}
}

// TestInitReportsAFileItCannotWrite: the directive file and main.go are
// written only where nothing exists, and a path that exists only as a
// dangling symlink cannot be written, which init reports.
func TestInitReportsAFileItCannotWrite(t *testing.T) {
	for _, name := range []string{generateFileName, mainFileName} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mustWriteFile(t, dir, "go.mod", "module example.com/initsymlink\n\ngo 1.23\n")
			if err := os.Symlink(filepath.Join(dir, "no", "such", "dir"), filepath.Join(dir, name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			var err error
			captureStdout(t, func() { err = runInit(dir, nil) })
			if err == nil {
				t.Fatalf("runInit wrote through a dangling %s symlink without an error", name)
			}
		})
	}
}
