package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/okian/servo/v3/internal/load"
)

// cfg is the load.Config every command takes, for the common test case of
// caring only about the directory and leaving the build flags at their
// defaults.
func cfg(dir string) load.Config { return load.Config{Dir: dir} }

// taggedCfg is cfg with a -tags value, for the variant tests.
func taggedCfg(dir, tags string) load.Config {
	return load.Config{Dir: dir, Build: load.BuildFlags{Tags: tags}}
}

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

// writeVariantModule materializes a module wired two ways: the default
// configuration binds store.Store to *memory.Mem, and the `prod`
// configuration binds it to *postgres.PG. Each implementation lives behind
// the matching build constraint, so neither type exists in the other
// configuration — which is exactly why the wiring needs a spec file per
// variant rather than one spec with a flag: a single spec naming both
// concrete types could not type-check in either configuration.
func writeVariantModule(t *testing.T, modulePath string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module `+modulePath+`

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)

	write("store/store.go", `package store

type Store interface{ Get(key string) string }
`)

	write("memory/memory.go", `//go:build !prod

package memory

type Mem struct{}

func (m *Mem) Get(key string) string { return "memory" }

func New() *Mem { return &Mem{} }
`)

	write("postgres/postgres.go", `//go:build prod

package postgres

import "context"

type PG struct{}

func (p *PG) Get(key string) string          { return "postgres" }
func (p *PG) Init(ctx context.Context) error { return nil }
func (p *PG) Stop(ctx context.Context) error { return nil }

func New() *PG { return &PG{} }
`)

	write("api/api.go", `package api

import (
	"context"

	"`+modulePath+`/store"
)

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }

func (s *Server) Run(ctx context.Context) error { <-ctx.Done(); return nil }
`)

	// Compiles in both configurations: New is declared by whichever
	// generated variant the build selects.
	write("cmd/app/main.go", `package main

import (
	"context"
	"fmt"
)

func main() {
	a, err := New(context.Background())
	fmt.Println(a, err)
}
`)

	write("cmd/app/spec.go", `//go:build servoinject && !prod

package main

import (
	"`+modulePath+`/api"
	"`+modulePath+`/memory"
	"`+modulePath+`/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
	)
}
`)

	write("cmd/app/spec_prod.go", `//go:build servoinject && prod

package main

import (
	"`+modulePath+`/api"
	"`+modulePath+`/postgres"
	"`+modulePath+`/store"
	"github.com/okian/servo/v3/servo"
)

func wireProd() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *postgres.PG](),
	)
}
`)

	runGoModTidy(t, dir)
	return dir
}

// runGoBuild builds the fixture module under the given tags, failing the
// test with the compiler's own output. This is the assertion that matters
// for variants: a generated file whose build constraint or provider
// references are wrong compiles nowhere, and no amount of inspecting
// servo's output would catch it.
func runGoBuild(t *testing.T, dir, tags string) {
	t.Helper()
	args := []string{"build"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	cmd := exec.Command("go", append(args, "./...")...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build -tags=%q in fixture: %v\n%s", tags, err, out)
	}
}

// writeTwoInjectorVariantModule is the multi-injector shape the docs
// recommend: cmd/app has a default and a prod spec, cmd/worker has only a
// default one. Generating the whole module with --tags=prod must succeed
// for cmd/app while leaving cmd/worker without a generated New — a real
// gap in the author\'s setup that only doctor is positioned to report.
func writeTwoInjectorVariantModule(t *testing.T, modulePath string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module `+modulePath+`

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)

	write("impl/impl.go", `package impl

type Store struct{}

func New() *Store { return &Store{} }
`)

	spec := func(constraint, fn string) string {
		return `//go:build ` + constraint + `

package main

import (
	"` + modulePath + `/impl"
	"github.com/okian/servo/v3/servo"
)

func ` + fn + `() {
	servo.Build(
		servo.Root[*impl.Store](),
	)
}
`
	}
	main := `package main

import "context"

func main() { _, _ = New(context.Background()) }
`

	write("cmd/app/main.go", main)
	write("cmd/app/spec.go", spec("servoinject && !prod", "wire"))
	write("cmd/app/spec_prod.go", spec("servoinject && prod", "wireProd"))

	write("cmd/worker/main.go", main)
	write("cmd/worker/spec.go", spec("servoinject && !prod", "wire"))

	runGoModTidy(t, dir)
	return dir
}
