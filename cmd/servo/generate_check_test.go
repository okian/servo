package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"golang.org/x/tools/go/packages"
)

func TestGenerateThenCheckRoundTrips(t *testing.T) {
	dir := writeAppModule(t, "example.com/roundtrip", true, "")

	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	out, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	if !strings.Contains(string(out), "func New(ctx context.Context) (*App, error)") {
		t.Errorf("generated file missing the New constructor:\n%s", out)
	}

	if err := runCheck(dir); err != nil {
		t.Fatalf("runCheck on a freshly generated file: %v", err)
	}
}

func TestCheckFailsWhenGeneratedFileMissing(t *testing.T) {
	dir := writeAppModule(t, "example.com/missing", true, "")

	err := runCheck(dir)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got err=%v, want a 'does not exist' error", err)
	}
}

func TestCheckFailsWhenGeneratedFileStale(t *testing.T) {
	dir := writeAppModule(t, "example.com/stale", true, "")
	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	if err := os.WriteFile(genPath, []byte("//go:build !servoinject\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCheck(dir)
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("got err=%v, want an 'is stale' error", err)
	}
	if !strings.Contains(err.Error(), "--- ") || !strings.Contains(err.Error(), "+++ ") {
		t.Errorf("expected the stale error to include a diff, got: %v", err)
	}
}

func TestGenerateEmitsTestAppWhenOverrideDeclared(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/override\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	mustWriteFile(t, dir, "api/api.go", `package api

import "example.com/override/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/override/api"
	"example.com/override/memory"
	"example.com/override/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
		servo.Override[store.Store, *memory.Mem](),
	)
}
`)
	runGoModTidy(t, dir)

	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	testGenPath := filepath.Join(dir, "cmd", "app", generatedTestFileName)
	out, err := os.ReadFile(testGenPath)
	if err != nil {
		t.Fatalf("expected %s to be written for a spec declaring Override: %v", generatedTestFileName, err)
	}
	if !strings.Contains(string(out), "func NewTestApp(ctx context.Context) (*TestApp, error)") {
		t.Errorf("generated test file missing NewTestApp:\n%s", out)
	}

	if err := runCheck(dir); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
}

func TestGenerateAndCheckProcessEveryInjectorInScope(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/multi\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "worker/worker.go", "package worker\n\ntype Consumer struct{}\n\nfunc New() *Consumer { return &Consumer{} }\n")
	mustWriteFile(t, dir, "cmd/apisvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multi/api"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*api.Server]()) }
`)
	mustWriteFile(t, dir, "cmd/workersvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multi/worker"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*worker.Consumer]()) }
`)
	runGoModTidy(t, dir)

	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	for _, svc := range []string{"apisvc", "workersvc"} {
		if _, err := os.Stat(filepath.Join(dir, "cmd", svc, generatedFileName)); err != nil {
			t.Errorf("expected %s to be generated for %s: %v", generatedFileName, svc, err)
		}
	}
	if err := runCheck(dir); err != nil {
		t.Fatalf("runCheck across both injectors: %v", err)
	}
}

// TestGenerateProcessesHealthyInjectorDespiteASiblingBeingBroken covers a
// multi-injector module where one injector has a real resolution
// diagnostic and another is healthy: runGenerate/runCheck loop over every
// pipeline and collect errors via errors.Join rather than stopping at the
// first, but that had never been exercised with one broken + one healthy
// injector in the same run — only all-healthy or (via other tests)
// single-injector failure.
func TestGenerateProcessesHealthyInjectorDespiteASiblingBeingBroken(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/mixedhealth\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "healthy/healthy.go", "package healthy\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\ntype Thing struct{}\n\n// NewThing takes an unresolvable dependency: no provider produces int.\nfunc NewThing(missing int) *Thing { return &Thing{} }\n")
	mustWriteFile(t, dir, "cmd/healthysvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/mixedhealth/healthy"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*healthy.Server]()) }
`)
	mustWriteFile(t, dir, "cmd/brokensvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/mixedhealth/broken"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*broken.Thing]()) }
`)
	runGoModTidy(t, dir)

	err := runGenerate(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for int") {
		t.Fatalf("got err=%v, want it to mention the broken injector's missing int provider", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "cmd", "healthysvc", generatedFileName)); statErr != nil {
		t.Errorf("healthy injector's servo_gen.go should still be written despite the sibling failing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "cmd", "brokensvc", generatedFileName)); statErr == nil {
		t.Error("broken injector must not have a servo_gen.go written")
	}

	// runCheck must behave the same way: report the broken one, but still
	// treat the healthy one as passing (no complaint about it).
	if err := runGenerate(dir); err == nil {
		t.Fatal("expected runGenerate to keep failing on the still-broken injector")
	}
	checkErr := runCheck(dir)
	if checkErr == nil || !strings.Contains(checkErr.Error(), "no provider for int") {
		t.Fatalf("got err=%v, want runCheck to also mention the broken injector", checkErr)
	}
	if strings.Contains(checkErr.Error(), "healthysvc") {
		t.Errorf("runCheck error mentions the healthy injector, want only the broken one: %v", checkErr)
	}
}

// TestGenerateWorksUnderVendorMode covers `-mod=vendor`: internal/load's
// packages.Config sets no vendor-specific flags of its own, so this only
// works if it correctly inherits the ambient GOFLAGS/vendor state from the
// environment rather than fighting it. Runs go mod vendor for real, so
// this is slower than the package's other tests.
func TestGenerateWorksUnderVendorMode(t *testing.T) {
	dir := writeAppModule(t, "example.com/vendored", true, "")

	cmd := exec.Command("go", "mod", "vendor")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod vendor: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "vendor", "modules.txt")); err != nil {
		t.Fatalf("expected a vendor/modules.txt after go mod vendor: %v", err)
	}

	t.Setenv("GOFLAGS", "-mod=vendor")
	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate under -mod=vendor: %v", err)
	}
	if err := runCheck(dir); err != nil {
		t.Fatalf("runCheck under -mod=vendor: %v", err)
	}
}

func TestGenerateReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous", false, "")

	err := runGenerate(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}

// TestGenerateWorksForNonMainPackageInjector covers an injector living in
// an ordinary, importable library package rather than package main —
// nothing in the loader or emitter actually requires package main; that's
// only a property `--dir`-based multi-injector isolation happens to rely
// on ("a package main can never import another package main"), not a
// hard requirement on the injector itself.
func TestGenerateWorksForNonMainPackageInjector(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/libinjector\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "wiring/spec.go", `//go:build servoinject

package wiring

import (
	"example.com/libinjector/api"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)

	if err := runGenerate(dir); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	genPath := filepath.Join(dir, "wiring", generatedFileName)
	out, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("reading generated file: %v", err)
	}
	if !strings.Contains(string(out), "package wiring") {
		t.Errorf("generated file should declare package wiring, got:\n%s", out)
	}
	if !strings.Contains(string(out), "func New(ctx context.Context) (*App, error)") {
		t.Errorf("generated file missing the New constructor:\n%s", out)
	}

	if err := runCheck(dir); err != nil {
		t.Fatalf("runCheck on the non-main injector: %v", err)
	}
}

func TestCheckReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous2", false, "")

	err := runCheck(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}

func TestRunCheckFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runCheck(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

// TestCheckOneFailsWhenGeneratedFileIsADirectory covers checkOne's
// os.ReadFile error branch that ISN'T os.IsNotExist — every other check
// test either has no generated file at all (IsNotExist) or a real one, so
// this reaches the "exists but unreadable as a file" case by putting a
// directory where servo_gen.go should be.
func TestCheckOneFailsWhenGeneratedFileIsADirectory(t *testing.T) {
	dir := writeAppModule(t, "example.com/checkgenisdir", true, "")
	p, err := buildPipeline(dir)
	if err != nil {
		t.Fatalf("buildPipeline: %v", err)
	}
	outPath := filepath.Join(filepath.Dir(p.spec.Pos.Filename), generatedFileName)
	if err := os.Mkdir(outPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err = checkOne(p)
	if err == nil || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got err=%v, want a real read error (not 'does not exist') since the path is a directory", err)
	}
}

// TestCheckOneFailsWhenEmitFails and TestGenerateOneFailsWhenEmitFails
// cover checkOne/generateOne's own emit.Emit error branches directly: a
// hand-built *pipeline with an illegal package name ("type", a Go keyword)
// and an empty graph (so p.resolve(nil) trivially succeeds with zero
// nodes) reaches the same "package type" formatting failure
// internal/emit's own tests trigger the identical way — nothing in a real,
// type-checked module can produce an illegal package name, so this is the
// only way to reach it.
func TestCheckOneFailsWhenEmitFails(t *testing.T) {
	spec := &load.Spec{InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/badpkgname"}}
	p := &pipeline{spec: spec, caps: graph.EmptyCapabilities(), scope: map[string]bool{}}

	err := checkOne(p)
	if err == nil || !strings.Contains(err.Error(), "failed to format") {
		t.Fatalf("got err=%v, want a 'failed to format' error", err)
	}
}

func TestGenerateOneFailsWhenEmitFails(t *testing.T) {
	spec := &load.Spec{InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/badpkgname2"}}
	p := &pipeline{spec: spec, caps: graph.EmptyCapabilities(), scope: map[string]bool{}}

	err := generateOne(p)
	if err == nil || !strings.Contains(err.Error(), "failed to format") {
		t.Fatalf("got err=%v, want a 'failed to format' error", err)
	}
}

// TestGenerateOneFailsWhenOutputDirIsReadOnly covers writeFileAtomic's
// error propagating out of generateOne: os.CreateTemp cannot create the
// temp file at all when the target directory itself isn't writable.
func TestGenerateOneFailsWhenOutputDirIsReadOnly(t *testing.T) {
	dir := writeAppModule(t, "example.com/genreadonlydir", true, "")
	p, err := buildPipeline(dir)
	if err != nil {
		t.Fatalf("buildPipeline: %v", err)
	}
	specDir := filepath.Dir(p.spec.Pos.Filename)
	if err := os.Chmod(specDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(specDir, 0o755) }() // best-effort restore so t.TempDir cleanup can remove it

	if err := generateOne(p); err == nil {
		t.Fatal("expected writeFileAtomic to fail against a read-only directory")
	}
}

// TestGenerateOneFailsWhenOverrideResolutionFails covers generateOne's
// second, override-scoped resolve call failing independently of the
// first: the primary Bind (to Mem) resolves fine, but the servotest
// Override names a concrete type with no provider anywhere, so only the
// second resolve — never the first — fails.
func TestGenerateOneFailsWhenOverrideResolutionFails(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/badoverride\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	mustWriteFile(t, dir, "missing/missing.go", "package missing\n\ntype Ghost struct{}\n")
	mustWriteFile(t, dir, "api/api.go", `package api

import "example.com/badoverride/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/badoverride/api"
	"example.com/badoverride/memory"
	"example.com/badoverride/missing"
	"example.com/badoverride/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
		servo.Override[store.Store, *missing.Ghost](),
	)
}
`)
	runGoModTidy(t, dir)

	err := runGenerate(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for *example.com/badoverride/missing.Ghost") {
		t.Fatalf("got err=%v, want a 'no provider' error naming the override's dangling target", err)
	}
}

// TestGeneratedCodeCompilesWithComponentsNamedLikeNewsOwnLocals covers
// every way a component's name can collide with an identifier the App
// half of the emitter derives rather than allocates.
//
// New declares `a`, `ctx` and `err` as bare identifiers in the same scope
// every node's variable lands in, so components named A, Ctx or Err used
// to emit `a := app.NewA()` beside `a := &App{}`. Separately, a node's
// variable name decides a stop<F> method and <F>StopOnce/<F>StopResult
// fields, so Foo and StopFoo gave App a method and a field of one name —
// and StartupReport collides with App's own startupReport field.
// `servo generate` reported success in every case, because it formats its
// output rather than type-checking it, and the compiler rejected it.
//
// This builds the module for real rather than inspecting the emitted
// text: the failure being guarded against is a compile error, so a
// compiler is the only honest witness.
func TestGeneratedCodeCompilesWithComponentsNamedLikeNewsOwnLocals(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)

	mustWriteFile(t, dir, "go.mod", `module example.com/localsapp

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	mustWriteFile(t, dir, "app/app.go", `package app

import "context"

type A struct{}

func NewA() *A { return &A{} }

type Ctx struct{}

func NewCtx() *Ctx { return &Ctx{} }

type Err struct{}

func NewErr() *Err { return &Err{} }

type StartupReport struct{}

func NewStartupReport() *StartupReport { return &StartupReport{} }

// Foo and StopFoo collide the other way: an App node's variable name
// decides a stop<F> method as well as <F>StopOnce/<F>StopResult fields,
// so StopFoo would take the field name Foo's method already has.
type Foo struct{}

func NewFoo() *Foo { return &Foo{} }

func (f *Foo) Stop(ctx context.Context) error { return nil }

type StopFoo struct{}

func NewStopFoo() *StopFoo { return &StopFoo{} }

func (s *StopFoo) Stop(ctx context.Context) error { return nil }

type Holder struct{}

func NewHolder(a *A, c *Ctx, e *Err, sr *StartupReport, f *Foo, sf *StopFoo) *Holder {
	return &Holder{}
}
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/localsapp/app"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.Root[*app.Holder](),
)
`)
	runGoModTidy(t, dir)

	if err := runGenerate(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		gen, _ := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}

// TestGeneratedCodeCompilesWhenANodeIsNamedAfterItsPackage: New declares
// every node's variable in one flat scope, so a component named after its
// own package — Store in package store — hides that package from every
// line after it. The declaration itself is fine, since the right-hand
// side resolves before the variable exists, which is why a package
// providing exactly one node always worked and why this stayed latent
// until one provided two.
func TestGeneratedCodeCompilesWhenANodeIsNamedAfterItsPackage(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)

	mustWriteFile(t, dir, "go.mod", `module example.com/shadowapp

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	mustWriteFile(t, dir, "store/store.go", `package store

type Store struct{}

func New() *Store { return &Store{} }

// Cache is constructed after Store, so calling store.NewCache is what a
// shadowed package identifier breaks.
type Cache struct{}

func NewCache(s *Store) *Cache { return &Cache{} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/shadowapp/store"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.Root[*store.Cache](),
)
`)
	runGoModTidy(t, dir)

	if err := runGenerate(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		gen, _ := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}
