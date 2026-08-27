package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
