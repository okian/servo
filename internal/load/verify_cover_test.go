package load

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestVerifyChecksAgainstTheOverlay: the generated code is checked against
// the files go build will see, and under --overlay those are the overlay's.
// Here the overlay changes the type the generated file returns a constant
// as, which only a check that applies it can catch. A package Verify is
// handed with no generated file in it is not judged at all, however broken
// it is, and an overlay file that cannot be read stops the check.
func TestVerifyChecksAgainstTheOverlay(t *testing.T) {
	dir := writeVerifyModule(t)
	mustWriteFile(t, dir, "cmd/app/port.go", "package main\n\ntype Port int\n")
	mustWriteFile(t, dir, "lib/lib.go", "package lib\n\nvar _ int = \"not the generated file's\"\n")
	replacements := t.TempDir()
	mustWriteFile(t, replacements, "port.go", "package main\n\ntype Port string\n")
	overlay := writeOverlayFile(t, replacements, map[string]string{filepath.Join(dir, "cmd", "app", "port.go"): filepath.Join(replacements, "port.go")})
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	files := map[string][]byte{gen: []byte("package main\n\nfunc New() Port { return 8080 }\n")}
	pkgs := []string{"example.com/verify/cmd/app", "example.com/verify/lib"}

	v, err := Verify(Config{Dir: dir}, files, pkgs)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Problems) != 0 {
		t.Fatalf("problems without the overlay = %v, want none: Port is an int on disk, and lib holds no generated file", v.Problems)
	}

	v, err = Verify(Config{Dir: dir, Build: BuildFlags{Overlay: overlay}}, files, pkgs)
	if err != nil {
		t.Fatalf("Verify(--overlay): %v", err)
	}
	if len(v.Problems) != 1 || !strings.HasPrefix(v.Problems[0], gen+":3:") {
		t.Fatalf("problems under the overlay = %v, want the one in the generated file: Port is a string there", v.Problems)
	}

	_, err = Verify(Config{Dir: dir, Build: BuildFlags{Overlay: filepath.Join(replacements, "missing.json")}}, files, pkgs)
	if err == nil || !strings.Contains(err.Error(), "reading -overlay file") {
		t.Fatalf("Verify(a missing overlay) = %v, want the overlay file's error", err)
	}
}

// TestVerifyReportsAGoCommandThatWillNotRun: when the go command refuses
// to run at all, there is nothing Verify could judge, and that is an error
// in the go command's words, not a clean bill of health.
func TestVerifyReportsAGoCommandThatWillNotRun(t *testing.T) {
	dir := writeVerifyModule(t)
	t.Setenv("GOFLAGS", "bogus")
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	_, err := Verify(Config{Dir: dir}, map[string][]byte{gen: []byte("package main\n\nfunc New() int { return 1 }\n")}, []string{"example.com/verify/cmd/app"})
	if err == nil || !strings.HasPrefix(err.Error(), "verify: ") || !strings.Contains(err.Error(), `parsing $GOFLAGS: non-flag "bogus"`) {
		t.Fatalf("Verify = %v, want the go command's own refusal", err)
	}
}

// TestVerifyReportsAnExportDataBuildThatFails: with the build cache off,
// the go command cannot build the dependencies' export data and lists no
// package, which go/packages hands back as an empty, error-free answer.
// That is not a verification: it is reported in the go command's words.
func TestVerifyReportsAnExportDataBuildThatFails(t *testing.T) {
	dir := writeVerifyModule(t)
	t.Setenv("GOCACHE", "off")
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{gen: []byte("package main\n\nfunc New() int { return 1 }\n")}, []string{"example.com/verify/cmd/app"})
	if err == nil || !strings.HasPrefix(err.Error(), "verify: go list -export failed: ") || !strings.Contains(err.Error(), "build cache is disabled by GOCACHE=off") {
		t.Fatalf("Verify = %+v, %v; want the go command's reason it could not build export data", v, err)
	}
}

// TestUnresolvedImportFromTheTypeChecker: with no list error on the
// dependency itself, the type checker's "could not import" is the reason
// a package cannot be judged, folded onto one line — unless it is an
// import cycle, which is the code's own doing and a problem to report.
func TestUnresolvedImportFromTheTypeChecker(t *testing.T) {
	missing := &packages.Package{Errors: []packages.Error{{Kind: packages.TypeError, Msg: "could not import example.com/gone (missing go.sum entry for module providing package example.com/gone;\n\tto add: go get example.com/gone)"}}}
	const want = "could not import example.com/gone (missing go.sum entry for module providing package example.com/gone; to add: go get example.com/gone)"
	if reason, ok := unresolvedImport(missing); !ok || reason != want {
		t.Errorf("unresolvedImport = %q, %v; want %q", reason, ok, want)
	}
	cycle := &packages.Package{Errors: []packages.Error{{Kind: packages.TypeError, Msg: "could not import example.com/b (import cycle: [example.com/a example.com/b])"}}}
	if reason, ok := unresolvedImport(cycle); ok {
		t.Errorf("unresolvedImport(an import cycle) = %q, want it left to problems", reason)
	}
}

// TestListAndCycleProblemsKeepToTheGeneratedFiles covers the go list errors
// that are not the generated files' doing — unpositioned, positioned in the
// user's own file, a cycle the package is not part of, a cycle no generated
// file closes — and both spellings of a cycle one does close, which render
// alike. A generated file that does not parse names no import at all.
func TestListAndCycleProblemsKeepToTheGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "app", "servo_gen.go")
	garbled := filepath.Join(dir, "app", "servo_garbled_gen.go")
	g := indexGenerated(dir, map[string][]byte{
		gen:     []byte("package app\n\nimport \"example.com/plugin\"\n"),
		garbled: []byte("not go at all"),
	})
	p := &packages.Package{PkgPath: "example.com/app"}
	mine := []string{garbled, gen}
	for _, e := range []packages.Error{
		{Kind: packages.ListError, Msg: "no position at all"},
		{Kind: packages.ListError, Pos: "app/main.go:3:8", Msg: "use of internal package example.com/x/internal/y not allowed"},
		{Kind: packages.ListError, Msg: "import cycle not allowed: import stack: [example.com/x example.com/y example.com/x]"},
		{Kind: packages.ListError, Msg: "import cycle not allowed: import stack: [example.com/app example.com/other example.com/app]"},
	} {
		if line, ok := g.listProblem(p, e, mine); ok {
			t.Errorf("listProblem(%q) = %q, want it left to the build", e.Msg, line)
		}
	}
	// go list closes its stack ([a b a]); the type checker does not ([a b]).
	want := gen + ":3:8: import cycle not allowed: example.com/app → example.com/plugin → example.com/app"
	for _, stack := range [][]string{
		{"example.com/app", "example.com/plugin", "example.com/app"},
		{"example.com/app", "example.com/plugin"},
	} {
		if got, ok := g.cycleProblem(p, stack, mine); !ok || got != want {
			t.Errorf("cycleProblem(%v) = %q, %v; want %q", stack, got, ok, want)
		}
	}
}
