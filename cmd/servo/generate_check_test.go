package main

import (
	"os"
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
	mustWriteFile(t, dir, "go.mod", "module example.com/override\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
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
	"github.com/okian/servo/v2/servo"
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
	mustWriteFile(t, dir, "go.mod", "module example.com/multi\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "worker/worker.go", "package worker\n\ntype Consumer struct{}\n\nfunc New() *Consumer { return &Consumer{} }\n")
	mustWriteFile(t, dir, "cmd/apisvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multi/api"
	"github.com/okian/servo/v2/servo"
)

func wire() { servo.Build(servo.Root[*api.Server]()) }
`)
	mustWriteFile(t, dir, "cmd/workersvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multi/worker"
	"github.com/okian/servo/v2/servo"
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

func TestGenerateReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous", false, "")

	err := runGenerate(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}

func TestCheckReportsResolutionErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/ambiguous2", false, "")

	err := runCheck(dir)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}
