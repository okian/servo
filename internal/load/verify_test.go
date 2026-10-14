package load

import (
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
	problems, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nfunc New() int { return \"not an int\" }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "servo_gen.go:3:") {
		t.Fatalf("problems = %v, want the one type error positioned in the generated file", problems)
	}
}

func TestVerifyAcceptsAGeneratedFileThatCompiles(t *testing.T) {
	dir := writeVerifyModule(t)
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	problems, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nfunc New() int { return 1 }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

// TestVerifyIgnoresErrorsOutsideTheGeneratedFiles: main.go referencing a
// New that is not there is the user's pre-generation state, not servo's
// mistake, and must never be reported as one.
func TestVerifyIgnoresErrorsOutsideTheGeneratedFiles(t *testing.T) {
	dir := writeVerifyModule(t)
	other := filepath.Join(dir, "cmd", "app", "other_gen.go")
	problems, err := Verify(Config{Dir: dir}, map[string][]byte{
		other: []byte("package main\n\nfunc Other() int { return 1 }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none: the undefined New is main.go's, not the overlay's", problems)
	}
}

// TestVerifySkipsAPackageGoListCannotDescribe: the first generated file
// that imports errgroup, or a config decoder, lands in a module whose go.sum
// has no line for it yet. go list reports that, not the type checker, and
// `go mod tidy` is the fix — so the package is skipped, never failed.
func TestVerifySkipsAPackageGoListCannotDescribe(t *testing.T) {
	dir := writeVerifyModule(t)
	gen := filepath.Join(dir, "cmd", "app", "servo_gen.go")
	problems, err := Verify(Config{Dir: dir}, map[string][]byte{
		gen: []byte("package main\n\nimport _ \"example.com/not-a-module/pkg\"\n\nfunc New() int { return \"would be a type error\" }\n"),
	}, []string{"example.com/verify/cmd/app"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none: a package with a list error is not servo's to judge", problems)
	}
}

func TestErrorFile(t *testing.T) {
	for pos, want := range map[string]string{
		"/tmp/x/servo_gen.go:12:3": "/tmp/x/servo_gen.go",
		"/tmp/x/servo_gen.go:12":   "/tmp/x/servo_gen.go",
		`C:\x\servo_gen.go:12:3`:   `C:\x\servo_gen.go`,
		"":                         "",
		"-":                        "",
	} {
		if got := errorFile(pos); got != want {
			t.Errorf("errorFile(%q) = %q, want %q", pos, got, want)
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
