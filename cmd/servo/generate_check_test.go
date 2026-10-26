package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"golang.org/x/tools/go/packages"
)

func TestGenerateThenCheckRoundTrips(t *testing.T) {
	dir := writeAppModule(t, "example.com/roundtrip", true, "")

	if err := runGenerate(cfg(dir)); err != nil {
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

	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("runCheck on a freshly generated file: %v", err)
	}
}

func TestCheckFailsWhenGeneratedFileMissing(t *testing.T) {
	dir := writeAppModule(t, "example.com/missing", true, "")

	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got err=%v, want a 'does not exist' error", err)
	}
}

func TestCheckFailsWhenGeneratedFileStale(t *testing.T) {
	dir := writeAppModule(t, "example.com/stale", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	if err := os.WriteFile(genPath, []byte("//go:build !servoinject\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runCheck(cfg(dir))
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

	if err := runGenerate(cfg(dir)); err != nil {
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

	if err := runCheck(cfg(dir)); err != nil {
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

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	for _, svc := range []string{"apisvc", "workersvc"} {
		if _, err := os.Stat(filepath.Join(dir, "cmd", svc, generatedFileName)); err != nil {
			t.Errorf("expected %s to be generated for %s: %v", generatedFileName, svc, err)
		}
	}
	if err := runCheck(cfg(dir)); err != nil {
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

	err := runGenerate(cfg(dir))
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
	if err := runGenerate(cfg(dir)); err == nil {
		t.Fatal("expected runGenerate to keep failing on the still-broken injector")
	}
	checkErr := runCheck(cfg(dir))
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
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate under -mod=vendor: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("runCheck under -mod=vendor: %v", err)
	}
}

func TestGenerateReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous", false, "")

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Fatalf("got err=%v, want a 'ambiguous provider for' ambiguity diagnostic", err)
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

	if err := runGenerate(cfg(dir)); err != nil {
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

	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("runCheck on the non-main injector: %v", err)
	}
}

func TestCheckReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous2", false, "")

	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Fatalf("got err=%v, want a 'ambiguous provider for' ambiguity diagnostic", err)
	}
}

func TestRunCheckFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runCheck(cfg(filepath.Join(t.TempDir(), "does-not-exist")))
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
	p, err := buildPipeline(cfg(dir))
	if err != nil {
		t.Fatalf("buildPipeline: %v", err)
	}
	outPath := filepath.Join(filepath.Dir(p.spec.Pos.Filename), generatedFileName)
	if err := os.Mkdir(outPath, 0o755); err != nil {
		t.Fatal(err)
	}

	err = checkPipeline(p)
	if err == nil || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("got err=%v, want a real read error (not 'does not exist') since the path is a directory", err)
	}
}

// TestCheckOneFailsWhenEmitFails and TestPlanOneFailsWhenEmitFails
// cover checkOne/planOne's own emit.Emit error branches directly: a
// hand-built *pipeline with an illegal package name ("type", a Go keyword)
// and an empty graph (so p.resolve(nil) trivially succeeds with zero
// nodes) reaches the same "package type" formatting failure
// internal/emit's own tests trigger the identical way — nothing in a real,
// type-checked module can produce an illegal package name, so this is the
// only way to reach it.
func TestCheckOneFailsWhenEmitFails(t *testing.T) {
	spec := &load.Spec{InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/badpkgname"}}
	p := &pipeline{spec: spec, caps: graph.EmptyCapabilities(), scope: map[string]bool{}}

	err := checkPipeline(p)
	if err == nil || !strings.Contains(err.Error(), "failed to format") {
		t.Fatalf("got err=%v, want a 'failed to format' error", err)
	}
}

func TestPlanOneFailsWhenEmitFails(t *testing.T) {
	spec := &load.Spec{InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/badpkgname2"}}
	p := &pipeline{spec: spec, caps: graph.EmptyCapabilities(), scope: map[string]bool{}}

	resolved, err := p.resolve(nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, err = planOne(p, resolved)
	if err == nil || !strings.Contains(err.Error(), "failed to format") {
		t.Fatalf("got err=%v, want a 'failed to format' error", err)
	}
}

// TestGenerateFailsWhenOutputDirIsReadOnly covers writeFileAtomic's error
// propagating out of the write: os.CreateTemp cannot create the temp file
// at all when the target directory itself isn't writable.
func TestGenerateFailsWhenOutputDirIsReadOnly(t *testing.T) {
	dir := writeAppModule(t, "example.com/genreadonlydir", true, "")
	p, err := buildPipeline(cfg(dir))
	if err != nil {
		t.Fatalf("buildPipeline: %v", err)
	}
	specDir := filepath.Dir(p.spec.Pos.Filename)
	if err := os.Chmod(specDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(specDir, 0o755) }() // best-effort restore so t.TempDir cleanup can remove it

	resolved, err := p.resolve(nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := planOne(p, resolved)
	if err != nil {
		t.Fatalf("planOne: %v", err)
	}
	if err := newFileWriter(io.Discard, false).apply(out); err == nil {
		t.Fatal("expected writeFileAtomic to fail against a read-only directory")
	}
}

// TestGenerateWritesNothingWhenTheOverrideGraphFails covers generate's
// second, override-scoped resolve call failing independently of the
// first: the primary Bind (to Mem) resolves fine, but the servotest
// Override names a concrete type with no provider anywhere, so only the
// second resolve — never the first — fails.
//
// That second resolve used to run after servo_gen.go was written, leaving
// a rewritten production file beside a missing test variant, and its
// diagnostic read like a production one, though the production graph
// resolved fine. Now nothing is written and the message says which graph.
func TestGenerateWritesNothingWhenTheOverrideGraphFails(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/badoverride\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	// Ghost implements Store, so the Override is well-formed; it just has
	// no constructor.
	mustWriteFile(t, dir, "missing/missing.go", "package missing\n\ntype Ghost struct{}\n\nfunc (g *Ghost) Get(key string) string { return \"\" }\n")
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

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "no provider for *example.com/badoverride/missing.Ghost") {
		t.Fatalf("got err=%v, want a 'no provider' error naming the override's dangling target", err)
	}
	for _, want := range []string{"in the NewTestApp graph (servo.Override at ", "spec.go:17:3)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say which graph failed — want %q in:\n%v", want, err)
		}
	}
	for _, name := range []string{generatedFileName, generatedTestFileName} {
		if _, statErr := os.Stat(filepath.Join(dir, "cmd", "app", name)); !os.IsNotExist(statErr) {
			t.Errorf("%s was written by a generation whose override graph failed (stat err = %v)", name, statErr)
		}
	}

	if err := runCheck(cfg(dir)); err == nil || !strings.Contains(err.Error(), "in the NewTestApp graph (servo.Override at ") {
		t.Errorf("check = %v, want the same failure, labelled the same way", err)
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

go 1.26.0

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

	if err := runGenerate(cfg(dir)); err != nil {
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

go 1.26.0

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

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		gen, _ := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}

// mustCompileGeneratedModule generates for dir and then builds it, which is
// the only honest witness for this family of bugs: every one of them exits
// 0 from `servo generate` and fails in the compiler, inside the one file
// users are told not to read.
func mustCompileGeneratedModule(t *testing.T, dir string) {
	t.Helper()
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		gen, _ := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}

// scratchModule writes the go.mod and the main/spec pair every case below
// shares, rooted at the type named by root.
func scratchModule(t *testing.T, module, importPath, root string) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module "+module+`

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+`
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	`+strconv.Quote(importPath)+`
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.Root[`+root+`](),
)
`)
	return dir
}

// TestGeneratedCodeCompilesWithAComponentNamedAfterACleanupField: a
// provider returning (T, func()) makes the App carry a <field>Cleanup
// field derived from the node's own variable name. Only the variable was
// ever reserved, so a second component whose name allocates to that same
// derived identifier redeclared it.
func TestGeneratedCodeCompilesWithAComponentNamedAfterACleanupField(t *testing.T) {
	dir := scratchModule(t, "example.com/cleanupapp", "example.com/cleanupapp/comp", "*comp.Root")
	mustWriteFile(t, dir, "comp/comp.go", `package comp

type Foo struct{}

func NewFoo() (*Foo, func()) { return &Foo{}, func() {} }

// FooCleanup allocates the same identifier Foo's cleanup field derives.
type FooCleanup struct{}

func NewFooCleanup() *FooCleanup { return &FooCleanup{} }

type Root struct{}

func NewRoot(f *Foo, fc *FooCleanup) *Root { return &Root{} }
`)
	mustCompileGeneratedModule(t, dir)
}

// TestGeneratedCodeCompilesWithAComponentNamedLikeAnEmittedPackage: New's
// locals share one scope with the package identifiers its own body
// qualifies calls with, so a component named Servo took the local `servo`
// and shadowed the package every `servo.StartupNode` after it resolves
// against.
func TestGeneratedCodeCompilesWithAComponentNamedLikeAnEmittedPackage(t *testing.T) {
	dir := scratchModule(t, "example.com/identapp", "example.com/identapp/comp", "*comp.Root")
	mustWriteFile(t, dir, "comp/comp.go", `package comp

import "context"

type Servo struct{}

func NewServo() *Servo { return &Servo{} }

func (s *Servo) Init(ctx context.Context) error { return nil }

type Time struct{}

func NewTime() *Time { return &Time{} }

type Errors struct{}

func NewErrors() *Errors { return &Errors{} }

type Sync struct{}

func NewSync() *Sync { return &Sync{} }

type Root struct{}

func NewRoot(s *Servo, ti *Time, e *Errors, sy *Sync) *Root { return &Root{} }

func (r *Root) Init(ctx context.Context) error { return nil }
`)
	mustCompileGeneratedModule(t, dir)
}

// TestGeneratedCodeCompilesWithAUserPackageNamedLikeTheStandardLibrary:
// the import manager could not alias a single-segment path, so the stdlib
// errors import arriving after a user package of that name was rendered
// under the identifier already taken.
func TestGeneratedCodeCompilesWithAUserPackageNamedLikeTheStandardLibrary(t *testing.T) {
	dir := scratchModule(t, "example.com/stdnameapp", "example.com/stdnameapp/errors", "*errors.Reporter")
	mustWriteFile(t, dir, "errors/errors.go", `package errors

import "context"

type Reporter struct{}

func NewReporter() *Reporter { return &Reporter{} }

// Init is what pulls the stdlib errors import in, via the Init phase's
// errors.Join on the rollback path.
func (r *Reporter) Init(ctx context.Context) error { return nil }
`)
	mustCompileGeneratedModule(t, dir)
}

// TestGeneratedCodeCompilesWhenAProviderReturnsAnotherPackagesType: the
// shadow guard compared later nodes' result-type packages, but the call it
// protects is qualified by the provider function's package. The two
// coincide only for the idiomatic `package foo; func New() *foo.Foo`,
// which is exactly what the sibling test above covers.
func TestGeneratedCodeCompilesWhenAProviderReturnsAnotherPackagesType(t *testing.T) {
	dir := scratchModule(t, "example.com/factoryapp", "example.com/factoryapp/widget", "*widget.Widget")
	mustWriteFile(t, dir, "widget/widget.go", `package widget

type Widget struct{}
`)
	mustWriteFile(t, dir, "factory/factory.go", `package factory

import "example.com/factoryapp/widget"

type Factory struct{}

func NewFactory() *Factory { return &Factory{} }

// NewWidget is qualified by factory at the call site while its result type
// is qualified by widget, so a node named factory shadows the caller.
func NewWidget(f *Factory) *widget.Widget { return &widget.Widget{} }
`)
	mustCompileGeneratedModule(t, dir)
}

// TestRollbackSurvivesACancelledStartupContext is the behavioural guard on
// the rollback context, and it has to run the generated program: the golden
// files pin the emitted text, and the text was never the problem — handing
// that text an already-cancelled ctx was.
//
// Every main in the docs and the examples passes New the signal context, so
// a SIGTERM arriving mid-startup cancels it. RunStop derives its budget from
// whatever it is given, so a cancelled parent means Done is closed before
// the select runs and every node is reported abandoned with its Drain, Flush
// and Stop never getting a chance to do anything.
//
// The signal arrives inside B's Init here, as it would in production: New
// must fail with the interruption, and B's own failure must survive beside
// it rather than be buried under the unwind.
func TestRollbackSurvivesACancelledStartupContext(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)

	mustWriteFile(t, dir, "go.mod", `module example.com/rollback

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	mustWriteFile(t, dir, "comp/comp.go", `package comp

import (
	"context"
	"errors"
	"fmt"
)

// A is constructed and initialised first, and is the node whose Stop has to
// run even though the context that started the unwind is already dead.
type A struct{}

func NewA() *A { return &A{} }

func (a *A) Init(ctx context.Context) error { return nil }

func (a *A) Stop(ctx context.Context) error {
	fmt.Println("A.Stop ran")
	return nil
}

// Signal stands in for the SIGTERM: main sets it to the cancel func of the
// context New was given, and B's Init fires it, mid-startup.
var Signal func()

// B initialises after A, while the signal arrives, and fails, which is
// what triggers the rollback.
type B struct{}

func NewB(a *A) *B { return &B{} }

func (b *B) Init(ctx context.Context) error {
	Signal()
	return errors.New("B cannot reach its backend")
}
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

import (
	"context"
	"fmt"

	"example.com/rollback/comp"
)

func main() {
	// Exactly the state a SIGTERM during startup leaves New in.
	ctx, cancel := context.WithCancel(context.Background())
	comp.Signal = cancel

	if _, err := New(ctx); err != nil {
		fmt.Println("New failed:", err)
		return
	}
	fmt.Println("New unexpectedly succeeded")
}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/rollback/comp"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.Root[*comp.B](),
)
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cmd := exec.Command("go", "run", "./cmd/app")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "A.Stop ran") {
		t.Errorf("the rollback did not stop the already-initialised node — it was handed a dead context:\n%s", got)
	}
	// And the failure that caused the unwind has to survive it, not be
	// buried under a node-by-node wall of "abandoned: context canceled" —
	// beside the interruption, which New reports because its context ended
	// before startup finished.
	if !strings.Contains(got, "B cannot reach its backend") {
		t.Errorf("the real startup error did not survive the rollback:\n%s", got)
	}
	if !strings.Contains(got, "startup interrupted") {
		t.Errorf("New did not report that its context ended mid-startup:\n%s", got)
	}
	if strings.Contains(got, "abandoned") {
		t.Errorf("a node was abandoned during a rollback that had a full budget:\n%s", got)
	}
}

// TestInitFailureWithACleanRollbackReturnsTheBareError: Report satisfies
// error by value, so errors.Join never skips it, and a clean report's
// Error() is "" — which errors.Join still separates with a newline.
func TestInitFailureWithACleanRollbackReturnsTheBareError(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)

	mustWriteFile(t, dir, "go.mod", `module example.com/joined

go 1.26.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	mustWriteFile(t, dir, "comp/comp.go", `package comp

import (
	"context"
	"errors"
)

type A struct{}

func NewA() *A { return &A{} }

func (a *A) Init(ctx context.Context) error { return errors.New("connection refused") }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

import (
	"context"
	"fmt"
)

func main() {
	_, err := New(context.Background())
	fmt.Printf("%q\n", err.Error())
}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/joined/comp"
	"github.com/okian/servo/v3/servo"
)

var _ = servo.Build(
	servo.Root[*comp.A](),
)
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}

	cmd := exec.Command("go", "run", "./cmd/app")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	// The node is named — an Init failure is a *servo.NodeError — and nothing
	// trails it.
	if got := strings.TrimSpace(string(out)); got != `"*example.com/joined/comp.A: Init: connection refused"` {
		t.Errorf("New returned %s, want the node-named error with no trailing newline from the clean report", got)
	}
}

// writeOverrideModule is the smallest module whose spec declares a
// servo.Override, so generate writes the test variant beside servo_gen.go.
// withOverride=false writes the same module without the Override line.
func writeOverrideModule(t *testing.T, withOverride bool) string {
	t.Helper()
	dir := t.TempDir()
	writeOverrideSpec(t, dir, withOverride)
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/leftover\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	mustWriteFile(t, dir, "api/api.go", `package api

import "example.com/leftover/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	runGoModTidy(t, dir)
	return dir
}

func writeOverrideSpec(t *testing.T, dir string, withOverride bool) {
	t.Helper()
	override := ""
	if withOverride {
		override = "\t\tservo.Override[store.Store, *memory.Mem](),\n"
	}
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/leftover/api"
	"example.com/leftover/memory"
	"example.com/leftover/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
`+override+`	)
}
`)
}

// TestCheckComparesTheTestVariant: the override variant used to be the one
// generated file check never looked at, so a stale NewTestApp was only
// ever caught by whichever go test happened to fail to compile it.
func TestCheckComparesTheTestVariant(t *testing.T) {
	dir := writeOverrideModule(t, true)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("runCheck on fresh output: %v", err)
	}

	testGen := filepath.Join(dir, "cmd", "app", generatedTestFileName)
	if err := os.WriteFile(testGen, []byte("//go:build !servoinject\n\n// Code generated by servo generate. DO NOT EDIT.\n\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), generatedTestFileName+" is stale") {
		t.Fatalf("got err=%v, want the test variant reported stale", err)
	}

	if err := os.Remove(testGen); err != nil {
		t.Fatal(err)
	}
	err = runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), generatedTestFileName+" does not exist") {
		t.Fatalf("got err=%v, want the test variant reported missing", err)
	}
}

// TestGenerateRemovesALeftoverTestVariant: once the last Override leaves
// the spec nothing regenerates servo_gen_test.go, yet it still compiles
// under go test and drifts from then on — the same orphan doctor reports
// for a tagged variant whose spec was deleted. generate now removes it,
// check refuses it, and doctor names it.
func TestGenerateRemovesALeftoverTestVariant(t *testing.T) {
	dir := writeOverrideModule(t, true)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	testGen := filepath.Join(dir, "cmd", "app", generatedTestFileName)
	if _, err := os.Stat(testGen); err != nil {
		t.Fatalf("expected the test variant to be written: %v", err)
	}

	writeOverrideSpec(t, dir, false)
	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "left over from a spec that no longer declares servo.Override") {
		t.Fatalf("got err=%v, want check to refuse the leftover test variant", err)
	}
	var doctorErr error
	out := captureStdout(t, func() { doctorErr = runDoctor(cfg(dir)) })
	if doctorErr == nil || !strings.Contains(out, "left over") {
		t.Fatalf("doctor err=%v, want the leftover named as a FAIL:\n%s", doctorErr, out)
	}

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate without the Override: %v", err)
	}
	if _, err := os.Stat(testGen); !os.IsNotExist(err) {
		t.Fatalf("expected generate to remove the leftover test variant, stat err=%v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("runCheck after the removal: %v", err)
	}

	// A test file servo did not write is never touched, whatever its name.
	if err := os.WriteFile(testGen, []byte("package main\n\n// hand-written, despite the name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate beside a hand-written file: %v", err)
	}
	if _, err := os.Stat(testGen); err != nil {
		t.Fatalf("generate removed a file it did not write: %v", err)
	}
}

// captureStderr is captureStdout for os.Stderr, where generate's notes,
// warnings and -v listing go.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	copied := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		copied <- buf.Bytes()
	}()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return string(<-copied)
}

// TestGenerateAndCheckReportAnImportTheCompilerRefuses: a provider under
// another tree's internal/ directory makes the generated file import a
// package its own package may not. That is go list's error, not the type
// checker's, and the compile check kept type errors only: generate exited
// 0, check said current, and `go build` failed inside servo_gen.go.
//
// Which stage stops it — the compile check, or resolution once it knows
// the internal/ rule — is not this test's concern. That both commands fail,
// name the package, and no longer call it a bug in servo, is.
func TestGenerateAndCheckReportAnImportTheCompilerRefuses(t *testing.T) {
	dir := scratchModule(t, "example.com/visapp", "example.com/visapp/svc", "*svc.Service")
	mustWriteFile(t, dir, "svc/svc.go", `package svc

import "example.com/visapp/svc/internal/db"

type Service struct{}

func NewService(d *db.DB) *Service { return &Service{} }
`)
	mustWriteFile(t, dir, "svc/internal/db/db.go", "package db\n\ntype DB struct{}\n\nfunc NewDB() *DB { return &DB{} }\n")
	runGoModTidy(t, dir)

	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate = nil, want a failure: the generated file cannot import svc/internal/db")
	}
	if !strings.Contains(err.Error(), "svc/internal/db") {
		t.Errorf("generate error does not name the internal package:\n%v", err)
	}
	if strings.Contains(err.Error(), "rather than in your code") {
		t.Errorf("generate still calls this a bug in servo:\n%v", err)
	}
	if err := runCheck(cfg(dir)); err == nil {
		t.Error("check = nil, want a failure: the module cannot build")
	}
}

// TestCheckCompilesTheFilesItFindsCurrent pins check's compile step on its
// own. A generated file byte-identical to a fresh generation can still not
// compile — a Bind to a mock whose interface has since grown a method
// generates the same broken file every time — and comparing bytes alone
// called that current, doctor too, while `go build` failed.
func TestCheckCompilesTheFilesItFindsCurrent(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/broken\n\ngo 1.23\n")
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() { _ = New() }\n")
	content := "package main\n\nfunc New() int { return \"not an int\" }\n"
	mustWriteFile(t, dir, "cmd/app/"+generatedFileName, content)

	c := newChecker(invocation{flags: []string{"--dir=" + dir}})
	gen := filepath.Join(dir, "cmd", "app", generatedFileName)
	c.current[gen] = []byte(content)
	c.pkgs = []string{"example.com/broken/cmd/app"}
	err := c.verify(cfg(dir))
	if err == nil {
		t.Fatal("verify = nil for a generated file that does not compile")
	}
	for _, want := range []string{
		"servo check: the generated code does not compile",
		gen + ":3:",
		"a servo.Bind or servo.Override type that no longer implements its interface",
		"then run `servo generate --dir=" + dir + "` again",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verify error does not contain %q:\n%v", want, err)
		}
	}
}

// writeMovableConfigModule is a module whose one //servo:config type lives
// in package cfgold and is consumed by svc.New, so the type can be moved to
// another package the way a refactor moves it.
func writeMovableConfigModule(t *testing.T, module string) string {
	t.Helper()
	dir := scratchModule(t, module, module+"/svc", "*svc.Service")
	writeConfigPackage(t, dir, "cfgold")
	mustWriteFile(t, dir, "svc/svc.go", "package svc\n\nimport \""+module+"/cfgold\"\n\ntype Service struct{}\n\nfunc New(c cfgold.Config) *Service { return &Service{} }\n")
	runGoModTidy(t, dir)
	return dir
}

func writeConfigPackage(t *testing.T, dir, pkg string) {
	t.Helper()
	mustWriteFile(t, dir, pkg+"/config.go", "package "+pkg+"\n\n//servo:config prefix=APP\ntype Config struct {\n\tdsn string `config:\"dsn,default=x\"`\n}\n")
}

// TestGenerateRemovesAnOrphanedCompanion: moving a //servo:config type to
// another package left its generated loader behind — still compiled into
// every build, naming a type no longer there. go build failed with
// "undefined: Config" while check and doctor reported the module current,
// because they compared only the companions a graph still used.
func TestGenerateRemovesAnOrphanedCompanion(t *testing.T) {
	const module = "example.com/movedcfg"
	dir := writeMovableConfigModule(t, module)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	old := filepath.Join(dir, "cfgold", "servo_config_gen.go")
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("expected the companion beside cfgold.Config: %v", err)
	}

	// The move: cfgold keeps nothing but its companion, which the
	// servoinject load cannot see at all.
	if err := os.Remove(filepath.Join(dir, "cfgold", "config.go")); err != nil {
		t.Fatal(err)
	}
	writeConfigPackage(t, dir, "cfgnew")
	mustWriteFile(t, dir, "svc/svc.go", "package svc\n\nimport \""+module+"/cfgnew\"\n\ntype Service struct{}\n\nfunc New(c cfgnew.Config) *Service { return &Service{} }\n")
	// Two files generate must leave alone: one with servo's name but not
	// its header, and a real companion in a package this configuration
	// cannot see, which holds other files — whether its type still exists
	// is a question for the configuration that can.
	mustWriteFile(t, dir, "handwritten/servo_config_gen.go", "package handwritten\n")
	mustWriteFile(t, dir, "gated/gated.go", "//go:build prod\n\npackage gated\n")
	mustWriteFile(t, dir, "gated/servo_config_gen.go", "//go:build !servoinject\n\n// Code generated by servo generate. DO NOT EDIT.\n\npackage gated\n")

	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), old+" is left over from a //servo:config type its package no longer declares") {
		t.Fatalf("check = %v, want the orphaned companion reported", err)
	}

	var genErr error
	stderr := captureStderr(t, func() { genErr = runGenerate(cfg(dir)) })
	if genErr != nil {
		t.Fatalf("generate after the move: %v", genErr)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the orphaned companion is still there (stat err = %v)", err)
	}
	if !strings.Contains(stderr, "servo generate: removed "+old+" — its package no longer declares a //servo:config type") {
		t.Errorf("the removal was not reported:\n%s", stderr)
	}
	for _, kept := range []string{"handwritten/servo_config_gen.go", "gated/servo_config_gen.go"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("generate removed %s, which it has no grounds to: %v", kept, err)
		}
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check after the removal: %v", err)
	}
	runGoBuild(t, dir, "")
}

// TestGenerateRemovesACompanionWhoseDirectiveWasDropped is the other shape
// of the same orphan: the package stays, with its files, but no longer
// declares a //servo:config type — so the load sees it, and finds none.
func TestGenerateRemovesACompanionWhoseDirectiveWasDropped(t *testing.T) {
	const module = "example.com/droppedcfg"
	dir := writeMovableConfigModule(t, module)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	mustWriteFile(t, dir, "cfgold/config.go", "package cfgold\n\ntype Config struct{ DSN string }\n\nfunc New() Config { return Config{} }\n")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate after dropping the directive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cfgold", "servo_config_gen.go")); !os.IsNotExist(err) {
		t.Fatalf("the companion of a dropped directive is still there (stat err = %v)", err)
	}
	runGoBuild(t, dir, "")
}

// TestGenerateLeavesUnchangedFilesAlone: rewriting identical bytes still
// moves a file's mtime, and a watcher running `servo generate && go build`
// on every change rebuilt on its own output forever — eighteen rebuilds in
// twenty-five seconds with nothing edited.
func TestGenerateLeavesUnchangedFilesAlone(t *testing.T) {
	dir := writeOverrideModule(t, true)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	files := []string{filepath.Join(dir, "cmd", "app", generatedFileName), filepath.Join(dir, "cmd", "app", generatedTestFileName)}
	for _, path := range files {
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate again: %v", err)
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Errorf("%s was rewritten by a generation that changed nothing (mtime %v)", filepath.Base(path), info.ModTime())
		}
	}

	// A file that did change is still rewritten.
	if err := os.WriteFile(files[0], []byte("// stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate over a stale file: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check after regenerating the stale file: %v", err)
	}
}

// TestFileWriterListsWhatItDid is -v: every file named once, as written,
// unchanged or removed — and a removal named even without -v, since
// deleting a file nobody named should never be silent.
func TestFileWriterListsWhatItDid(t *testing.T) {
	dir := t.TempDir()
	a, b, gone := filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go"), filepath.Join(dir, "gone.go")
	mustWriteFile(t, dir, "b.go", "same")
	mustWriteFile(t, dir, "gone.go", "old")

	var log bytes.Buffer
	w := newFileWriter(&log, true)
	shared := &injectorOutput{files: map[string][]byte{a: []byte("new"), b: []byte("same")}, leftover: gone}
	if err := w.apply(shared); err != nil {
		t.Fatal(err)
	}
	// A second injector sharing a companion: handled, and listed, once.
	if err := w.apply(&injectorOutput{files: map[string][]byte{a: []byte("new")}}); err != nil {
		t.Fatal(err)
	}
	want := "servo generate: wrote " + a + "\n" +
		"servo generate: unchanged " + b + "\n" +
		"servo generate: removed " + gone + " — the spec no longer declares servo.Override\n"
	if log.String() != want {
		t.Errorf("-v listing =\n%s\nwant\n%s", log.String(), want)
	}

	log.Reset()
	mustWriteFile(t, dir, "gone.go", "old")
	quiet := newFileWriter(&log, false)
	if err := quiet.apply(&injectorOutput{files: map[string][]byte{a: []byte("newer")}, leftover: gone}); err != nil {
		t.Fatal(err)
	}
	if want := "servo generate: removed " + gone + " — the spec no longer declares servo.Override\n"; log.String() != want {
		t.Errorf("without -v, output =\n%s\nwant only the removal", log.String())
	}
}

// TestCheckAcceptsACRLFCheckout: Git for Windows checks text files out
// with CRLF line endings by default, and check compared raw bytes — every
// generated file was stale on every Windows checkout, with a diff of
// identical-looking lines and a note blaming another servo version.
func TestCheckAcceptsACRLFCheckout(t *testing.T) {
	dir := writeOverrideModule(t, true)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	gen := filepath.Join(dir, "cmd", "app", generatedFileName)
	lf, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	crlf := bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n"))
	if err := os.WriteFile(gen, crlf, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("check on a CRLF checkout = %v, want current", err)
	}

	// A real change in a CRLF file is still stale, and the diff shows that
	// change alone rather than every line.
	edited := bytes.Replace(crlf, []byte("// Code generated by servo generate. DO NOT EDIT.\r\n"), []byte("// Code generated by servo generate. DO NOT EDIT. (edited)\r\n"), 1)
	if err := os.WriteFile(gen, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	err = runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("check = %v, want stale", err)
	}
	changed := 0
	for _, line := range strings.Split(err.Error(), "\n") {
		if (strings.HasPrefix(line, "-") || strings.HasPrefix(line, "+")) && !strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "+++") {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("the diff shows %d changed lines, want 2 (one out, one in):\n%v", changed, err)
	}
}

// TestCheckSuggestsTheFlagsItWasRunWith: the stale and missing messages
// named a bare `servo generate`, which run where CI ran — the repository
// root, with --dir — fails with "run `servo init`".
func TestCheckSuggestsTheFlagsItWasRunWith(t *testing.T) {
	dir := writeVariantModule(t, "example.com/suggestflags")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	err := run([]string{"check", "--dir", dir, "--tags", "prod"})
	if want := "`servo generate --dir=" + dir + " --tags=prod`"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("check = %v, want the remediation %s", err, want)
	}
}

func TestInvocationOf(t *testing.T) {
	parse := func(args ...string) invocation {
		t.Helper()
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		fs.String("dir", ".", "")
		registerBuildFlags(fs)
		fs.Bool("v", false, "")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		return invocationOf(fs)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "`servo generate`"},
		{[]string{"--dir", "."}, "`servo generate`"},
		{[]string{"-v", "--tags=dev"}, "`servo generate --tags=prod`"},
		{[]string{"--dir=svc/api", "--mod=vendor"}, "`servo generate --dir=svc/api --mod=vendor --tags=prod`"},
		{[]string{"--dir", "my app"}, "`servo generate --dir='my app' --tags=prod`"},
	} {
		tags := []string{"prod"}
		if len(c.args) == 0 || c.args[0] == "--dir" && c.args[1] == "." {
			tags = nil
		}
		if got := parse(c.args...).command("generate", tags); got != c.want {
			t.Errorf("args %q: command = %s, want %s", c.args, got, c.want)
		}
	}
}

// TestGenerateNotesAnEmptyGraph: `servo init` scaffolds an empty
// servo.Build(), which generates and builds cleanly into a program whose
// Run returns the moment it starts — with nothing to say why.
func TestGenerateNotesAnEmptyGraph(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/empty\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc wire() {\n\tservo.Build()\n}\n")
	runGoModTidy(t, dir)

	var err error
	stderr := captureStderr(t, func() { err = runGenerate(cfg(dir)) })
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(stderr, "example.com/empty/cmd/app declares no servo.Root and no servo.HTTP()") {
		t.Errorf("no note for an empty graph:\n%s", stderr)
	}

	full := writeOverrideModule(t, false)
	stderr = captureStderr(t, func() { err = runGenerate(cfg(full)) })
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if stderr != "" {
		t.Errorf("generate of a graph with roots printed:\n%s", stderr)
	}
}

// TestGenerateWarnsWhenItCannotCompileItsOutput: the first generated
// import of a config decoder lands in a module that does not require it
// yet. The compile check cannot judge that package — `go mod tidy` is the
// fix — and it used to skip it in silence, so generate succeeded on a file
// `go build` then rejected. It still succeeds, but says so.
func TestGenerateWarnsWhenItCannotCompileItsOutput(t *testing.T) {
	dir := writeMovableConfigModule(t, "example.com/yamlcfg")
	spec := readFile(t, filepath.Join(dir, "cmd", "app", "spec.go"))
	mustWriteFile(t, dir, "cmd/app/spec.go", strings.Replace(spec, "servo.Root[", "servo.ConfigFile(\"config.yaml\"),\n\tservo.Root[", 1))

	var err error
	stderr := captureStderr(t, func() { err = runGenerate(cfg(dir)) })
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, want := range []string{
		"servo generate: warning: could not compile the generated code in example.com/yamlcfg/cmd/app: ",
		"gopkg.in/yaml.v3",
		"run `go mod tidy`, then `servo generate` again",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not contain %q:\n%s", want, stderr)
		}
	}
}
