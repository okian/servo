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

// writeVariantInjectorModule materializes the shape that makes
// InjectorsInOtherConfigurations necessary: a module where one injector
// has both a default and a prod spec and the other has only a default one.
// Under -tags=prod, cmd/worker is not an injector — and its main.go still
// calls the New only a generated file supplies.
func writeVariantInjectorModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", "module example.com/variantinjectors\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	write("worker/worker.go", "package worker\n\ntype Consumer struct{}\n\nfunc New() *Consumer { return &Consumer{} }\n")

	write("cmd/api/main.go", "package main\n\nfunc main() {}\n")
	spec := func(tag, root, pkg string) string {
		return "//go:build servoinject && " + tag + `

package main

import (
	"example.com/variantinjectors/` + pkg + `"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*` + root + `](),
	)
}
`
	}
	write("cmd/api/spec_default.go", spec("!prod", "api.Server", "api"))
	write("cmd/api/spec_prod.go", spec("prod", "api.Server", "api"))

	// The pre-generation state every injector passes through: main.go
	// calls the constructor `servo generate` writes, which under -tags=prod
	// nothing generates for this package.
	write("cmd/worker/main.go", `package main

import "context"

func main() {
	app, err := New(context.Background())
	_, _ = app, err
}
`)
	write("cmd/worker/spec.go", spec("!prod", "worker.Consumer", "worker"))
	// A non-Go source excluded by a constraint, which go/packages reports
	// in the same list as the excluded spec file. It cannot be parsed as
	// Go at all, so the scan has to pass over it rather than let it stand
	// as evidence — either way — about whether this package holds a spec.
	write("cmd/worker/stub_purego.s", "//go:build purego\n")

	runGoModTidy(t, dir)
	return dir
}

// TestInjectorsInOtherConfigurationsNamesTheSpecsThisRunCannotSee covers
// both directions of the check, because both are load-bearing.
//
// Under -tags=prod, cmd/worker's only spec is excluded by its own !prod
// constraint, so it is not an injector here: servo generates nothing for
// it, its pre-generation "undefined: New" is not a reason to refuse to
// generate cmd/api, and the user still has to be told — whether cmd/worker
// should grow a prod variant or be gated out of the prod build is the
// author's call, but silence turns it into a compiler error much later.
//
// Under the default configuration the situation is reversed: cmd/worker is
// an ordinary injector, and it is cmd/api that has a spec this run cannot
// see.
func TestInjectorsInOtherConfigurationsNamesTheSpecsThisRunCannotSee(t *testing.T) {
	dir := writeVariantInjectorModule(t)
	const (
		apiPkg    = "example.com/variantinjectors/cmd/api"
		workerPkg = "example.com/variantinjectors/cmd/worker"
	)

	prod, err := Load(Config{Dir: dir, Build: BuildFlags{Tags: "prod"}})
	if err != nil {
		t.Fatalf("Load(-tags=prod): %v", err)
	}
	specs, err := FindSpecs(prod)
	if err != nil {
		t.Fatalf("FindSpecs(-tags=prod): %v", err)
	}
	if len(specs) != 1 || specs[0].InjectorPkg.PkgPath != apiPkg {
		t.Fatalf("got %d specs under -tags=prod, want only %s", len(specs), apiPkg)
	}
	got := prod.InjectorsInOtherConfigurations(apiPkg)
	if len(got) != 1 || got[0] != workerPkg {
		t.Errorf("InjectorsInOtherConfigurations = %v, want [%s]", got, workerPkg)
	}
	// The claim above is only worth anything if cmd/worker really does
	// fail to build here, which is what makes ignoring its errors a
	// decision rather than a coincidence.
	if worker := prod.ByPath[workerPkg]; worker == nil || len(worker.Errors) == 0 {
		t.Fatalf("fixture is not exercising anything: %s loads cleanly under -tags=prod", workerPkg)
	}
	if err := prod.NonInjectorErrors(apiPkg); err != nil {
		t.Errorf("NonInjectorErrors = %v, want nil: a package whose spec this run cannot see is not this run's problem", err)
	}

	def, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load(default): %v", err)
	}
	got = def.InjectorsInOtherConfigurations()
	if len(got) != 1 || got[0] != apiPkg {
		t.Errorf("InjectorsInOtherConfigurations() under the default configuration = %v, want [%s]: cmd/worker's only spec is visible here, so it is not hiding one", got, apiPkg)
	}
	if got := def.InjectorsInOtherConfigurations(apiPkg, workerPkg); len(got) != 0 {
		t.Errorf("InjectorsInOtherConfigurations(both injectors) = %v, want nothing: a package already known to be an injector must not be reported as one elsewhere", got)
	}
}
