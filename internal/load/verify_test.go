package load

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeVerifyModule is the smallest module Verify can be pointed at: a
// main package whose main.go calls a New that only an overlaid generated
// file supplies — the exact shape a fresh injector has.
func writeVerifyModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/verify\n\ngo 1.23\n")
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() { _ = New() }\n")
	return dir
}

func TestVerifyReportsATypeErrorInAGeneratedFile(t *testing.T) {
	dir := writeVerifyModule(t)
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nfunc New() int { return \"not an int\" }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Problems) != 1 || !strings.Contains(v.Problems[0], "servo_gen.go:3:") {
		t.Fatalf("problems = %v, want the one type error positioned in the generated file", v.Problems)
	}
}

func TestVerifyAcceptsAGeneratedFileThatCompiles(t *testing.T) {
	dir := writeVerifyModule(t)
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nfunc New() int { return 1 }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Problems) != 0 || len(v.Unchecked) != 0 {
		t.Fatalf("got %+v, want nothing to report", v)
	}
}

// TestVerifyIgnoresErrorsOutsideTheGeneratedFiles: main.go referencing a
// New that is not there is the user's pre-generation state, not servo's
// mistake, and must never be reported as one.
func TestVerifyIgnoresErrorsOutsideTheGeneratedFiles(t *testing.T) {
	dir := writeVerifyModule(t)
	other := filepath.Join(dir, "cmd", "app", "other_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{
		other: []byte("package main\n\nfunc Other() int { return 1 }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Problems) != 0 {
		t.Fatalf("problems = %v, want none: the undefined New is main.go's, not the overlay's", v.Problems)
	}
}

// TestVerifyReportsAPackageGoListCannotDescribe: the first generated file
// that imports a config decoder lands in a module whose go.sum has no line
// for it yet. go list reports that, not the type checker, and `go mod tidy`
// is the fix — so the package is not judged. But it is named, with go's
// reason: skipping it silently let generate succeed on a file `go build`
// then rejected.
func TestVerifyReportsAPackageGoListCannotDescribe(t *testing.T) {
	dir := writeVerifyModule(t)
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nimport _ \"example.com/not-a-module/pkg\"\n\nfunc New() int { return \"would be a type error\" }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Problems) != 0 {
		t.Errorf("problems = %v, want none: a package with an unresolved import is not servo's to judge", v.Problems)
	}
	if len(v.Unchecked) != 1 {
		t.Fatalf("unchecked = %+v, want the one package", v.Unchecked)
	}
	if u := v.Unchecked[0]; u.PkgPath != "example.com/verify/cmd/app" || !strings.Contains(u.Reason, "example.com/not-a-module/pkg") || strings.Contains(u.Reason, "\n") {
		t.Errorf("unchecked = %+v, want the package and go's reason, on one line", u)
	}
}

// TestVerifyReportsAnInternalImport: "use of internal package … not
// allowed" is go list's error, not the type checker's, and it is positioned
// relative to the go command's working directory. Verify kept type errors
// only, so a provider under another tree's internal/ directory produced a
// file generate passed and `go build` refused. Run with --dir as given on
// the command line too, which is relative.
func TestVerifyReportsAnInternalImport(t *testing.T) {
	dir := writeVerifyModule(t)
	mustWriteFile(t, dir, "svc/internal/db/db.go", "package db\n\ntype DB struct{}\n")
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	content := "package main\n\nimport \"example.com/verify/svc/internal/db\"\n\nfunc New() *db.DB { return nil }\n"
	// On disk as well as overlaid, as generate and check have it. A
	// relative --dir under a symlinked path (/var on macOS) puts the go
	// command in the resolved namespace, where an overlay keyed by the
	// unresolved path would never apply.
	mustWriteFile(t, dir, "cmd/app/servo_gen.go", content)
	files := map[string][]byte{gen: []byte(content)}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, rel} {
		v, err := Verify(Config{Dir: d}, files, []string{"example.com/verify/cmd/app"})
		if err != nil {
			t.Fatalf("Verify(Dir: %s): %v", d, err)
		}
		want := gen + ":3:8: use of internal package example.com/verify/svc/internal/db not allowed"
		if len(v.Problems) != 1 || v.Problems[0] != want {
			t.Errorf("Verify(Dir: %s) problems = %q, want [%q]", d, v.Problems, want)
		}
	}
}

// TestVerifyReportsAnImportCycleAtTheGeneratedImport: a provider whose
// package imports the injector's package makes the generated import close
// a cycle. go list positions that nowhere, so it was dropped, and what was
// left — the type checker's "invalid type" echoes of it — read as a bug in
// servo. It is now reported alone, the way go build reports it, at the
// import that closes the loop.
func TestVerifyReportsAnImportCycleAtTheGeneratedImport(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/cycle\n\ngo 1.23\n")
	mustWriteFile(t, dir, "app/app.go", "package app\n\ntype User struct{}\n\ntype Store interface{ Get() User }\n")
	mustWriteFile(t, dir, "plugin/plugin.go", "package plugin\n\nimport \"example.com/cycle/app\"\n\ntype PG struct{}\n\nfunc (*PG) Get() app.User { return app.User{} }\n")
	gen := filepath.Join(dir, "app", "servo_gen.go")
	v, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package app\n\nimport \"example.com/cycle/plugin\"\n\nvar _ Store = (*plugin.PG)(nil)\n"),
	}, []string{"example.com/cycle/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := gen + ":3:8: import cycle not allowed: example.com/cycle/app → example.com/cycle/plugin → example.com/cycle/app"
	if len(v.Problems) != 1 || v.Problems[0] != want {
		t.Fatalf("problems = %q, want only [%q]", v.Problems, want)
	}
}

func TestErrorFile(t *testing.T) {
	for pos, want := range map[string]string{
		"/tmp/x/servo_gen.go:12:3": "/tmp/x/servo_gen.go",
		"/tmp/x/servo_gen.go:12":   "/tmp/x/servo_gen.go",
		`C:\x\servo_gen.go:12:3`:   `C:\x\servo_gen.go`,
		"cmd/app/servo_gen.go:3:8": "cmd/app/servo_gen.go",
		"":                         "",
		"-":                        "",
	} {
		if got := errorFile(pos); got != want {
			t.Errorf("errorFile(%q) = %q, want %q", pos, got, want)
		}
	}
}

// TestCompilerProblems: the compiler's report arrives as one list error,
// headed with the package and positioned relative to the go command's
// directory, with indented continuation lines. Only the lines inside a
// generated file are kept, each with its continuation.
func TestCompilerProblems(t *testing.T) {
	dir := t.TempDir()
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	g := indexGenerated(dir, map[string][]byte{gen: nil})
	report := "# example.com/app/cmd/app\n" +
		"cmd/app/main.go:4:2: not ours\n" +
		"cmd/app/servo_gen.go:9:22: cannot use x as Store value\n" +
		"\thave Get() int\n" +
		"\twant Get() string\n" +
		"cmd/app/main.go:5:2: not ours either\n" +
		"\twith a continuation that is not ours"
	got := g.compilerProblems(report)
	want := gen + ":9:22: cannot use x as Store value\n\thave Get() int\n\twant Get() string"
	if len(got) != 1 || got[0] != want {
		t.Errorf("compilerProblems = %q, want [%q]", got, want)
	}
}

func TestImportCycle(t *testing.T) {
	for msg, want := range map[string]string{
		"import cycle not allowed: import stack: [a b a]":       "a b a",
		"could not import b (import cycle: [a b])":              "a b",
		"use of internal package example.com/x/internal/y":      "",
		"missing go.sum entry for module providing package x/y": "",
	} {
		if got := strings.Join(importCycle(msg), " "); got != want {
			t.Errorf("importCycle(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestGeneratedArgsLeaveTheSpecTagOut(t *testing.T) {
	if got := (BuildFlags{}).GeneratedArgs(); len(got) != 0 {
		t.Errorf("GeneratedArgs() = %v, want none for the default configuration", got)
	}
	got := (BuildFlags{Tags: "prod,servoinject", Mod: "vendor"}).GeneratedArgs()
	if strings.Join(got, " ") != "-tags=prod -mod=vendor" {
		t.Errorf("GeneratedArgs() = %v", got)
	}
}
