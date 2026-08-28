package load

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestLoadAndFindSpec(t *testing.T) {
	dir := writeFixtureModule(t, "")

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ServoPkg == nil {
		t.Fatal("ServoPkg not found")
	}
	if _, ok := loaded.ByPath["example.com/app/api"]; !ok {
		t.Fatal("expected example.com/app/api among loaded packages")
	}

	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}

	if len(spec.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(spec.Roots))
	}
	if want := "*example.com/app/api.Server"; spec.Roots[0].Key.String() != want {
		t.Errorf("root key = %s, want %s", spec.Roots[0].Key.String(), want)
	}

	if len(spec.Binds) != 1 {
		t.Fatalf("got %d binds, want 1", len(spec.Binds))
	}
	bind := spec.Binds[0]
	if want := "example.com/app/store.Store"; bind.Iface.String() != want {
		t.Errorf("bind iface = %s, want %s", bind.Iface.String(), want)
	}
	if want := "*example.com/app/memory.Mem"; bind.Concrete.String() != want {
		t.Errorf("bind concrete = %s, want %s", bind.Concrete.String(), want)
	}

	if len(spec.Overrides) != 0 {
		t.Errorf("got %d overrides, want 0", len(spec.Overrides))
	}
}

func TestFindSpecExtractsOverride(t *testing.T) {
	dir := writeFixtureModule(t, "\t\tservo.Override[store.Store, *memory.Mem](),\n")

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Overrides) != 1 {
		t.Fatalf("got %d overrides, want 1", len(spec.Overrides))
	}
}

func TestFindSpecNoBuildCall(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	writeFile := func(rel, content string) {
		mustWriteFile(t, dir, rel, content)
	}
	writeFile("go.mod", "module example.com/empty\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	writeFile("main.go", "package main\n\nimport _ \"github.com/okian/servo/v3/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "no servo.Build") {
		t.Fatalf("got err=%v, want a 'no servo.Build' error", err)
	}
}

func TestFindSpecMissingBuildTag(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/untagged\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `package spec

import "github.com/okian/servo/v3/servo"

type App struct{}

func Wire() {
	servo.Build()
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "missing a `//go:build servoinject`") {
		t.Fatalf("got err=%v, want a missing-build-tag error", err)
	}
}

// TestLoadReturnsErrorForInvalidDir covers the packages.Load hard-error
// path: a Dir the go command can't even chdir into fails before any
// package-level errors would apply, and Load must wrap and surface that
// rather than panic or return a nil, nil zero value.
func TestLoadReturnsErrorForInvalidDir(t *testing.T) {
	_, err := Load(Config{Dir: "/nonexistent/path/that/should/never/exist"})
	if err == nil || !strings.Contains(err.Error(), "load:") {
		t.Fatalf("got err=%v, want a wrapped load error", err)
	}
}

// TestLoadReturnsErrorWhenServoPackageNotImported covers a module that
// never imports the servo runtime package at all — not even a spec file
// with a missing build tag, just no reference anywhere. Load must fail
// with a clear diagnostic rather than proceeding with a nil ServoPkg that
// every caller assumes is non-nil.
func TestLoadReturnsErrorWhenServoPackageNotImported(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/noservo\n\ngo 1.23\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	_, err := Load(Config{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "servo runtime package") {
		t.Fatalf("got err=%v, want a 'servo runtime package ... not found' error", err)
	}
}

func TestPackagePathOf(t *testing.T) {
	dir := writeFixtureModule(t, "")
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	path, ok := PackagePathOf(spec.Roots[0].Type)
	if !ok || path != "example.com/app/api" {
		t.Fatalf("PackagePathOf(root) = %q, %v; want example.com/app/api, true", path, ok)
	}
}

func TestNonInjectorErrorsIgnoresExcludedPackages(t *testing.T) {
	broken := &packages.Package{PkgPath: "example.com/broken", Errors: []packages.Error{{Msg: "undefined: New"}}}
	clean := &packages.Package{PkgPath: "example.com/clean"}
	l := &Loaded{All: []*packages.Package{broken, clean}}

	if err := l.NonInjectorErrors("example.com/broken"); err != nil {
		t.Errorf("NonInjectorErrors excluding the only broken package = %v, want nil", err)
	}
}

func TestNonInjectorErrorsReportsNonExcludedPackages(t *testing.T) {
	broken := &packages.Package{PkgPath: "example.com/broken", Errors: []packages.Error{{Msg: "undefined: New"}}}
	l := &Loaded{All: []*packages.Package{broken}}

	err := l.NonInjectorErrors()
	if err == nil || !strings.Contains(err.Error(), "undefined: New") {
		t.Fatalf("NonInjectorErrors() = %v, want an error mentioning the broken package", err)
	}
}

func TestNonInjectorErrorsExcludesMultipleInjectorsAtOnce(t *testing.T) {
	injectorA := &packages.Package{PkgPath: "example.com/cmd/a", Errors: []packages.Error{{Msg: "undefined: New"}}}
	injectorB := &packages.Package{PkgPath: "example.com/cmd/b", Errors: []packages.Error{{Msg: "undefined: New"}}}
	clean := &packages.Package{PkgPath: "example.com/lib"}
	l := &Loaded{All: []*packages.Package{injectorA, injectorB, clean}}

	if err := l.NonInjectorErrors("example.com/cmd/a", "example.com/cmd/b"); err != nil {
		t.Errorf("NonInjectorErrors excluding both injectors = %v, want nil", err)
	}
}

func TestImportClosure(t *testing.T) {
	dir := writeFixtureModule(t, "")
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	apiPkg := loaded.ByPath["example.com/app/api"]
	c := ImportClosure([]*packages.Package{apiPkg})
	if !c["example.com/app/store"] {
		t.Errorf("expected store package in api's import closure, got %v", c)
	}
	if c["example.com/app/memory"] {
		t.Errorf("memory should not be in api's import closure (api does not import memory), got %v", c)
	}
}
