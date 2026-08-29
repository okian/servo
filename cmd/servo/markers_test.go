package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// valueAndIncludeModule writes a two-injector module that uses both new
// markers the way they are meant to be used together: a shared marker set
// in its own package, declaring the value both injectors need.
func valueAndIncludeModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", `module example.com/mk

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+`
`)
	mustWriteFile(t, dir, "conf/conf.go", `package conf

// Flags is parsed in main, so nothing in the graph can build it.
type Flags struct{ DSN string }
`)
	mustWriteFile(t, dir, "store/store.go", `package store

type Store interface{ Get(key string) string }
`)
	mustWriteFile(t, dir, "postgres/postgres.go", `package postgres

import "example.com/mk/conf"

type DB struct{ dsn string }

func New(f conf.Flags) *DB { return &DB{dsn: f.DSN} }

func (d *DB) Get(key string) string { return d.dsn + ":" + key }
`)
	mustWriteFile(t, dir, "api/api.go", `package api

import "example.com/mk/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	mustWriteFile(t, dir, "wiring/wiring.go", `//go:build servoinject

package wiring

import (
	"example.com/mk/conf"
	"example.com/mk/postgres"
	"example.com/mk/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Value[conf.Flags](),
		servo.Bind[store.Store, *postgres.DB](),
	}
}
`)
	mustWriteFile(t, dir, "cmd/api/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/api/spec.go", `//go:build servoinject

package main

import (
	"example.com/mk/api"
	"example.com/mk/wiring"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`)
	mustWriteFile(t, dir, "cmd/worker/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/worker/spec.go", `//go:build servoinject

package main

import (
	"example.com/mk/postgres"
	"example.com/mk/wiring"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*postgres.DB](),
	)
}
`)
	return dir
}

// TestValueAndIncludeGenerateAndCompile is the end-to-end gate on both
// markers: one shared set spliced into two injectors in different
// packages, carrying a value neither of them can build.
func TestValueAndIncludeGenerateAndCompile(t *testing.T) {
	dir := valueAndIncludeModule(t)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}

	gen := mustRead(t, filepath.Join(dir, "cmd", "api", "servo_gen.go"))
	for _, want := range []string{
		"type Values struct {",
		"Flags conf.Flags",
		"func NewWith(ctx context.Context, v Values) (*App, error)",
		"func New(ctx context.Context) (*App, error)",
		"flags := v.Flags",
		"db := postgres.New(flags)",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated file missing %q:\n%s", want, gen)
		}
	}
	// The Include has to have reached the second injector too, or the
	// shared set is not shared.
	worker := mustRead(t, filepath.Join(dir, "cmd", "worker", "servo_gen.go"))
	if !strings.Contains(worker, "type Values struct {") {
		t.Errorf("the second injector did not receive the included markers:\n%s", worker)
	}

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated code does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// TestValueBeatsAProviderForTheSameType: declaring a value is how you say
// "this comes from the caller", which is only meaningful if it wins over a
// constructor that also produces the type.
func TestValueBeatsAProviderForTheSameType(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", `module example.com/vb

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+`
`)
	mustWriteFile(t, dir, "conf/conf.go", `package conf

type Flags struct{ DSN string }

// NewFlags is a perfectly good provider that the servo.Value overrides.
func NewFlags() Flags { return Flags{DSN: "from-provider"} }

type Thing struct{ F Flags }

func NewThing(f Flags) *Thing { return &Thing{F: f} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/vb/conf"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Value[conf.Flags](),
		servo.Root[*conf.Thing](),
	)
}
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	gen := mustRead(t, filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if strings.Contains(gen, "conf.NewFlags()") {
		t.Errorf("the provider won over servo.Value:\n%s", gen)
	}
	if !strings.Contains(gen, "flags := v.Flags") {
		t.Errorf("the supplied value was not used:\n%s", gen)
	}
}

// TestUnusedValueIsADiagnostic: an unused declaration would still add a
// field every caller has to supply and the app never reads.
func TestUnusedValueIsADiagnostic(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", `module example.com/uv

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+`
`)
	mustWriteFile(t, dir, "conf/conf.go", `package conf

type Flags struct{ DSN string }

type Thing struct{}

func NewThing() *Thing { return &Thing{} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/uv/conf"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Value[conf.Flags](),
		servo.Root[*conf.Thing](),
	)
}
`)
	runGoModTidy(t, dir)
	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate = nil, want a diagnostic for the unused servo.Value")
	}
	if !strings.Contains(err.Error(), "nothing in the graph depends on") {
		t.Errorf("diagnostic does not explain the problem:\n%v", err)
	}
}

// TestIncludeRejectsAnUntaggedMarkerSet: an included file's markers panic
// if they ever run, exactly as a spec file's do, so it needs the same tag.
func TestIncludeRejectsAnUntaggedMarkerSet(t *testing.T) {
	dir := valueAndIncludeModule(t)
	body := mustRead(t, filepath.Join(dir, "wiring", "wiring.go"))
	mustWriteFile(t, dir, "wiring/wiring.go", strings.Replace(body, "//go:build servoinject\n\n", "", 1))
	runGoModTidy(t, dir)

	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate = nil, want a refusal: the included marker set is not gated")
	}
	if !strings.Contains(err.Error(), "servoinject") {
		t.Errorf("refusal does not name the missing build tag:\n%v", err)
	}
}

// TestIncludeRejectsABodyItWouldHaveToRun: the spec is read as syntax and
// never executed, so a body servo would have to evaluate is refused rather
// than half-understood.
func TestIncludeRejectsABodyItWouldHaveToRun(t *testing.T) {
	dir := valueAndIncludeModule(t)
	mustWriteFile(t, dir, "wiring/wiring.go", `//go:build servoinject

package wiring

import (
	"example.com/mk/postgres"
	"example.com/mk/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	m := []servo.Marker{servo.Bind[store.Store, *postgres.DB]()}
	return m
}
`)
	runGoModTidy(t, dir)

	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate = nil, want a refusal for a body servo would have to execute")
	}
	if !strings.Contains(err.Error(), "read as syntax and never run") {
		t.Errorf("refusal does not explain why the shape is required:\n%v", err)
	}
}

// TestIncludeCycleIsADiagnostic: two sets including each other must report
// the path that closed the loop rather than recursing forever.
func TestIncludeCycleIsADiagnostic(t *testing.T) {
	dir := valueAndIncludeModule(t)
	mustWriteFile(t, dir, "wiring/wiring.go", `//go:build servoinject

package wiring

import "github.com/okian/servo/v3/servo"

func Shared() []servo.Marker {
	return []servo.Marker{servo.Include(Other)}
}

func Other() []servo.Marker {
	return []servo.Marker{servo.Include(Shared)}
}
`)
	runGoModTidy(t, dir)

	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate = nil, want an Include cycle diagnostic")
	}
	if !strings.Contains(err.Error(), "Include cycle") {
		t.Errorf("diagnostic does not name the cycle:\n%v", err)
	}
}

// TestLocalBindOverridesAnIncludedOne: the local file has the last word,
// which is the only ordering that makes a shared set worth having.
func TestLocalBindOverridesAnIncludedOne(t *testing.T) {
	dir := valueAndIncludeModule(t)
	// It takes the shared servo.Value too, so swapping the binding does
	// not also make that value unused — which is its own diagnostic, and
	// not the one under test here.
	mustWriteFile(t, dir, "memory/memory.go", `package memory

import "example.com/mk/conf"

type Store struct{ dsn string }

func New(f conf.Flags) *Store { return &Store{dsn: f.DSN} }

func (s *Store) Get(key string) string { return "memory:" + key }
`)
	mustWriteFile(t, dir, "cmd/api/spec.go", `//go:build servoinject

package main

import (
	"example.com/mk/api"
	"example.com/mk/memory"
	"example.com/mk/store"
	"example.com/mk/wiring"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Bind[store.Store, *memory.Store](),
		servo.Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	gen := mustRead(t, filepath.Join(dir, "cmd", "api", "servo_gen.go"))
	if !strings.Contains(gen, "memory.New(flags)") {
		t.Errorf("the local Bind did not win over the included one:\n%s", gen)
	}
	if strings.Contains(gen, "postgres.New(") {
		t.Errorf("the included Bind is still in effect:\n%s", gen)
	}
}

// TestValueInTheOverrideVariant: the test App may resolve a different set,
// so it gets its own Values type and its own With constructor, both in the
// _test.go file beside the production pair.
func TestValueInTheOverrideVariant(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", `module example.com/tv

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+`
`)
	mustWriteFile(t, dir, "app/app.go", `package app

type Flags struct{ DSN string }

type Store interface{ Get() string }

type Real struct{ f Flags }

func NewReal(f Flags) *Real { return &Real{f: f} }

func (r *Real) Get() string { return r.f.DSN }

type Fake struct{}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Get() string { return "fake" }

type Server struct{ s Store }

func NewServer(s Store, f Flags) *Server { return &Server{s: s} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", `package main

func main() {}
`)
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/tv/app"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Value[app.Flags](),
		servo.Bind[app.Store, *app.Real](),
		servo.Override[app.Store, *app.Fake](),
		servo.Root[*app.Server](),
	)
}
`)
	runGoModTidy(t, dir)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	gen := mustRead(t, filepath.Join(dir, "cmd", "app", "servo_gen_test.go"))
	for _, want := range []string{
		"type TestValues struct {",
		"func NewTestAppWith(ctx context.Context, v TestValues) (*TestApp, error)",
		"func NewTestApp(ctx context.Context) (*TestApp, error)",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("override variant missing %q:\n%s", want, gen)
		}
	}

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated test variant does not compile: %v\n%s\n---\n%s", err, out, gen)
	}
}
