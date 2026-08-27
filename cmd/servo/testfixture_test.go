package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot returns this repo's module root, computed from this file's own
// path (cmd/servo/testfixture_test.go is two directories below root), so
// fixture modules can `replace` the servo module locally without a
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

// writeAppModule materializes a small, real on-disk module with a
// multi-level graph and a genuinely ambiguous store.Store binding (both
// postgres.DB and memory.Mem implement it), so the fixture exercises
// levels, capabilities, an explicit Bind, and multiple roots at once:
//
//	logger.Logger (L1, Finalizer)
//	  -> postgres.DB (L2, Initializer+Finalizer+Healther) -- bound explicitly to store.Store
//	    -> api.Server (L3, Runner+Readier)               -- root
//	  -> worker.Consumer (L2, Runner)                     -- root
//
// includeBind controls whether the explicit servo.Bind[store.Store,
// *postgres.DB]() line is present — omitting it, with memory.Mem also
// implementing store.Store, produces a genuine ambiguity diagnostic.
// extraSpecArgs is inserted verbatim into the servo.Build(...) argument
// list, so callers can add servo.Override[...] for the servotest variant.
func writeAppModule(t *testing.T, modulePath string, includeBind bool, extraSpecArgs string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module `+modulePath+`

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)

	write("logger/logger.go", `package logger

import "context"

type Logger struct{}

func New() *Logger { return &Logger{} }

func (l *Logger) Stop(ctx context.Context) error { return nil }
`)

	write("store/store.go", `package store

type Store interface{ Get(key string) string }
`)

	write("memory/memory.go", `package memory

type Mem struct{}

func (m *Mem) Get(key string) string { return "" }

func New() *Mem { return &Mem{} }
`)

	write("postgres/postgres.go", `package postgres

import (
	"context"

	"`+modulePath+`/logger"
)

type DB struct{ log *logger.Logger }

func New(l *logger.Logger) (*DB, error) { return &DB{log: l}, nil }

func (d *DB) Get(key string) string { return "value" }

func (d *DB) Init(ctx context.Context) error  { return nil }
func (d *DB) Stop(ctx context.Context) error  { return nil }
func (d *DB) Health(ctx context.Context) error { return nil }
`)

	write("api/api.go", `package api

import (
	"context"

	"`+modulePath+`/store"
)

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }

func (s *Server) Run(ctx context.Context) error   { <-ctx.Done(); return nil }
func (s *Server) Ready(ctx context.Context) error { return nil }

// NewID is deliberately rejected (primitive result), so list --rejected
// has something real to report.
func NewID() string { return "" }
`)

	write("worker/worker.go", `package worker

import (
	"context"

	"`+modulePath+`/logger"
)

type Consumer struct{ log *logger.Logger }

func New(l *logger.Logger) *Consumer { return &Consumer{log: l} }

func (c *Consumer) Run(ctx context.Context) error { <-ctx.Done(); return nil }
`)

	bindLine := ""
	if includeBind {
		bindLine = "\t\tservo.Bind[store.Store, *postgres.DB](),\n"
	}
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"`+modulePath+`/api"
	"`+modulePath+`/postgres"
	"`+modulePath+`/store"
	"`+modulePath+`/worker"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*worker.Consumer](),
`+bindLine+extraSpecArgs+`	)
}
`)

	runGoModTidy(t, dir)
	return dir
}
