package load

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot returns this repo's module root, computed from this file's own
// path (internal/load/testfixture_test.go is two directories below root),
// so fixture modules can `replace` the servo module locally without a
// hardcoded machine-specific path.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// mustWriteFile writes content to rel (relative to dir), creating parent
// directories as needed.
func mustWriteFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runGoModTidy resolves the fixture module's go.sum against the local
// module cache (the replaced servo module needs no checksum; its own
// dependencies are already cached from the outer module's build).
func runGoModTidy(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy in fixture: %v\n%s", err, out)
	}
}

// writeFixtureModule materializes a small, real on-disk module exercising
// an interface with one implementation needing an explicit Bind, so
// go/packages (which shells out to the go command and cannot work from
// in-memory ASTs alone) has something real to load.
func writeFixtureModule(t *testing.T, extraSpecArgs string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/app

go 1.23

require github.com/okian/servo/v2 v2.0.0

replace github.com/okian/servo/v2 => `+root+`
`)

	write("store/store.go", `package store

type Store interface{ Get(key string) string }
`)

	write("memory/memory.go", `package memory

type Mem struct{}

func (m *Mem) Get(key string) string { return "" }

func New() *Mem { return &Mem{} }
`)

	write("api/api.go", `package api

import "example.com/app/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)

	write("spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/app/api"
	"example.com/app/memory"
	"example.com/app/store"
	"github.com/okian/servo/v2/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
`+extraSpecArgs+`	)
}
`)

	runGoModTidy(t, dir)

	return dir
}
