package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okian/servo/v3/servo"
)

// writeTraceModule materializes a module that uses servo.Trace end to end
// against a stand-in for the OpenTelemetry trace API: two tiny modules
// under fakeotel/ take the real import paths through replace directives,
// so the generated code is compiled against exactly the identifiers it
// hard-codes while the test stays offline. The stand-in records every
// span, which is what main prints.
func writeTraceModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/tracefix

go 1.26.0

require (
	github.com/okian/servo/v3 v3.0.0
	go.opentelemetry.io/otel v1.0.0
	go.opentelemetry.io/otel/trace v1.0.0
)

replace (
	github.com/okian/servo/v3 => `+root+`
	go.opentelemetry.io/otel => ./fakeotel/otel
	go.opentelemetry.io/otel/trace => ./fakeotel/trace
)
`)
	write("fakeotel/otel/go.mod", "module go.opentelemetry.io/otel\n\ngo 1.23\n")
	write("fakeotel/otel/codes/codes.go", `package codes

type Code uint32

const (
	Unset Code = 0
	Error Code = 1
	Ok    Code = 2
)
`)
	write("fakeotel/trace/go.mod", "module go.opentelemetry.io/otel/trace\n\ngo 1.23\n\nrequire go.opentelemetry.io/otel v1.0.0\n")
	write("fakeotel/trace/trace.go", `package trace

import (
	"context"

	"go.opentelemetry.io/otel/codes"
)

type TracerProvider interface {
	Tracer(name string, options ...TracerOption) Tracer
}

type Tracer interface {
	Start(ctx context.Context, spanName string, opts ...SpanStartOption) (context.Context, Span)
}

type Span interface {
	End(options ...SpanEndOption)
	RecordError(err error, options ...EventOption)
	SetStatus(code codes.Code, description string)
}

type TracerOption interface{}
type SpanStartOption interface{}
type SpanEndOption interface{}
type EventOption interface{}
`)
	write("telemetry/telemetry.go", `package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Provider records one line per ended span: scope, name, and the status
// set on it, if any.
type Provider struct{ Lines []string }

func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return &tracer{p: p, scope: name}
}

func (p *Provider) Stop(ctx context.Context) error {
	p.Lines = append(p.Lines, "provider stopped")
	return nil
}

type tracer struct {
	p     *Provider
	scope string
}

type spanKey struct{}

func (t *tracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	s := &span{t: t, name: name}
	return context.WithValue(ctx, spanKey{}, s), s
}

type span struct {
	t      *tracer
	name   string
	status string
	err    error
}

func (s *span) End(_ ...trace.SpanEndOption) {
	line := s.t.scope + " " + s.name
	if s.err != nil {
		line += " recorded=" + s.err.Error()
	}
	if s.status != "" {
		line += " " + s.status
	}
	s.t.p.Lines = append(s.t.p.Lines, line)
}

func (s *span) RecordError(err error, _ ...trace.EventOption) { s.err = err }

func (s *span) SetStatus(c codes.Code, desc string) {
	s.status = fmt.Sprintf("status=%d(%s)", c, desc)
}

// Current names the span carried by ctx, so an implementation can prove it
// received the wrapper's context rather than the caller's.
func Current(ctx context.Context) string {
	if s, ok := ctx.Value(spanKey{}).(*span); ok {
		return s.name
	}
	return "none"
}
`)
	write("store/store.go", `package store

import (
	"context"
	"errors"
)

var ErrMissing = errors.New("store: missing")

type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Put(ctx context.Context, key, value string) error
	Close() error
}
`)
	write("postgres/postgres.go", `package postgres

import (
	"context"
	"errors"

	"example.com/tracefix/store"
	"example.com/tracefix/telemetry"
)

type DB struct {
	Seen   []string
	Events []string
}

func New() *DB { return &DB{} }

func (d *DB) Init(ctx context.Context) error { d.Events = append(d.Events, "init"); return nil }
func (d *DB) Stop(ctx context.Context) error { d.Events = append(d.Events, "stop"); return nil }

func (d *DB) Get(ctx context.Context, key string) (string, error) {
	d.Seen = append(d.Seen, telemetry.Current(ctx))
	if key == "missing" {
		return "", store.ErrMissing
	}
	return "value:" + key, nil
}

func (d *DB) Put(ctx context.Context, key, value string) error {
	d.Seen = append(d.Seen, telemetry.Current(ctx))
	return errors.New("boom")
}

func (d *DB) Close() error { d.Events = append(d.Events, "close"); return nil }
`)
	write("memory/memory.go", `package memory

import "context"

type Mem struct{}

func New() *Mem { return &Mem{} }

func (m *Mem) Get(ctx context.Context, key string) (string, error) { return "mem", nil }
func (m *Mem) Put(ctx context.Context, key, value string) error    { return nil }
func (m *Mem) Close() error                                        { return nil }
`)
	write("api/api.go", `package api

import (
	"context"

	"example.com/tracefix/store"
)

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }

// Exercise drives every shape: a hit, an expected miss, a failure, and a
// context-less method.
func (s *Server) Exercise(ctx context.Context) {
	_, _ = s.s.Get(ctx, "hit")
	_, _ = s.s.Get(ctx, "missing")
	_ = s.s.Put(ctx, "k", "v")
	_ = s.s.Close()
}
`)
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/tracefix/api"
	"example.com/tracefix/memory"
	"example.com/tracefix/postgres"
	"example.com/tracefix/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *postgres.DB](),
		servo.Override[store.Store, *memory.Mem](),
		servo.Trace[store.Store](servo.Expected(store.ErrMissing)),
	)
}
`)
	write("cmd/app/main.go", `package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

func main() {
	ctx := context.Background()
	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	app.server.Exercise(ctx)
	app.Shutdown(ctx)
	for _, line := range app.provider.Lines {
		fmt.Println(line)
	}
	fmt.Println("inner saw: " + strings.Join(app.db.Seen, ", "))
	fmt.Println("inner events: " + strings.Join(app.db.Events, ", "))
}
`)
	write("cmd/app/app_test.go", `package main

import (
	"context"
	"testing"
)

func TestTracedMock(t *testing.T) {
	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.server.Exercise(context.Background())
	if len(app.provider.Lines) != 3 {
		t.Fatalf("spans = %v, want three: the override is traced through the same wrapper", app.provider.Lines)
	}
}
`)
	runGoModTidy(t, dir)
	return dir
}

func TestGenerateTraceModule(t *testing.T) {
	dir := writeTraceModule(t)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("check after generate: %v", err)
	}

	gen, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	for _, want := range []string{
		`"go.opentelemetry.io/otel/codes"`,
		`"go.opentelemetry.io/otel/trace"`,
		"type tracedStore struct",
		`tp.Tracer("example.com/tracefix/store")`,
		`t.tracer.Start(ctx, "store.Store/Get")`,
		"if err != nil && !errors.Is(err, store.ErrMissing) {",
		"func (t *tracedStore) Close() error {\n\treturn t.inner.Close()\n}",
		"storeTraced := newTracedStore(db, provider)",
		"server := api.New(storeTraced)",
	} {
		if !strings.Contains(string(gen), want) {
			t.Errorf("servo_gen.go missing %q", want)
		}
	}
	testGen, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen_test.go"))
	if err != nil {
		t.Fatalf("read generated test file: %v", err)
	}
	if !strings.Contains(string(testGen), "storeTraced := newTestTracedStore(storeOverride, provider)") {
		t.Errorf("servo_gen_test.go does not trace the override through its own wrapper type:\n%s", testGen)
	}

	// The generated file is compiled and run against the stand-in trace
	// API: the span names, the expected error staying off the span, the
	// failure marked, the context-less method untraced, the wrapper's
	// context reaching the implementation, and the implementation keeping
	// its own lifecycle are all observable from the output.
	runGoModTidy(t, dir)
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet (which compiles servo_gen_test.go too): %v\n%s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "app")
	build := exec.Command("go", "build", "-o", bin, "./cmd/app")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build fixture: %v\n%s\n---\n%s", err, out, gen)
	}
	out, err := exec.Command(bin).CombinedOutput()
	if err != nil {
		t.Fatalf("run fixture: %v\n%s", err, out)
	}
	want := strings.Join([]string{
		"example.com/tracefix/store store.Store/Get",
		"example.com/tracefix/store store.Store/Get",
		"example.com/tracefix/store store.Store/Put recorded=boom status=1(boom)",
		"provider stopped",
		"inner saw: store.Store/Get, store.Store/Get, store.Store/Put",
		// Close is context-less: delegated to the implementation (which
		// records it) with no span of its own.
		"inner events: init, close, stop",
		"",
	}, "\n")
	if string(out) != want {
		t.Fatalf("fixture output:\n%s\nwant:\n%s", out, want)
	}
	test := exec.Command("go", "test", "./cmd/app")
	test.Dir = dir
	if out, err := test.CombinedOutput(); err != nil {
		t.Fatalf("go test fixture (NewTestApp traces the mock): %v\n%s", err, out)
	}
}

// TestTraceInInspectionCommands: the wrapper is a node like any other, so
// explain, why, graph and list all have to show it.
func TestTraceInInspectionCommands(t *testing.T) {
	dir := writeTraceModule(t)

	explain := captureStdout(t, func() {
		if err := runExplain(cfg(dir), "store.Store", false); err != nil {
			t.Fatalf("explain: %v", err)
		}
	})
	for _, want := range []string{
		"example.com/tracefix/store.Store#traced",
		"provider:     generated OpenTelemetry wrapper around *example.com/tracefix/postgres.DB",
		"binding:      traced",
		// Dependencies are listed by the node that answered them, so the
		// tracer provider appears as the concrete type its constructor
		// returns — exactly as an interface parameter does.
		"depends on:   *example.com/tracefix/postgres.DB, *example.com/tracefix/telemetry.Provider",
		"depended on:  *example.com/tracefix/api.Server",
		"capabilities: none",
	} {
		if !strings.Contains(explain, want) {
			t.Errorf("explain output missing %q:\n%s", want, explain)
		}
	}

	why := captureStdout(t, func() {
		if err := runWhy(cfg(dir), "postgres.DB", false); err != nil {
			t.Fatalf("why: %v", err)
		}
	})
	if !strings.Contains(why, "store.Store#traced") {
		t.Errorf("why output should walk through the wrapper to reach the implementation:\n%s", why)
	}

	graphOut := captureStdout(t, func() {
		if err := runGraph(cfg(dir), "json"); err != nil {
			t.Fatalf("graph: %v", err)
		}
	})
	var g servo.Graph
	if err := json.Unmarshal([]byte(graphOut), &g); err != nil {
		t.Fatalf("graph --format=json is not JSON: %v\n%s", err, graphOut)
	}
	var found bool
	for _, n := range g.Nodes {
		if n.Type == "example.com/tracefix/store.Store#traced" {
			found = true
			if n.Binding != "traced" || n.Wraps != "*example.com/tracefix/postgres.DB" || len(n.Capabilities) != 0 {
				t.Errorf("graph node = %+v, want binding traced, wraps *postgres.DB, no capabilities", n)
			}
		}
	}
	if !found {
		t.Errorf("graph JSON has no wrapper node:\n%s", graphOut)
	}

	list := captureStdout(t, func() {
		if err := runList(cfg(dir), false, false, false); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	if !strings.Contains(list, "traced:") || !strings.Contains(list, "example.com/tracefix/store.Store") || !strings.Contains(list, "expected: store.ErrMissing") {
		t.Errorf("list output missing the traced section:\n%s", list)
	}
}

// TestGenerateTraceWithoutProvider: the tracer provider is a dependency
// like any other, and the diagnostic for a missing one says whose job it
// is to write.
func TestGenerateTraceWithoutProvider(t *testing.T) {
	dir := writeTraceModule(t)
	mustWriteFile(t, dir, "telemetry/telemetry.go", `package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// Keep the import, drop the provider.
var _ trace.Tracer

func Current(ctx context.Context) string { return "" }
`)
	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate succeeded without a trace.TracerProvider provider")
	}
	for _, want := range []string{
		"no provider for go.opentelemetry.io/otel/trace.TracerProvider",
		"needed by the servo.Trace wrapper for example.com/tracefix/store.Store",
		"there is no global fallback",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnostic missing %q:\n%v", want, err)
		}
	}
}

// TestGenerateTraceWithoutOtel: a module that never imports the trace API
// cannot have a wrapper written against it, and the diagnostic names the
// import that brings it in.
func TestGenerateTraceWithoutOtel(t *testing.T) {
	dir := writeTraceModule(t)
	mustWriteFile(t, dir, "telemetry/telemetry.go", `package telemetry

type Provider struct{}

func NewProvider() *Provider { return &Provider{} }
`)
	mustWriteFile(t, dir, "postgres/postgres.go", `package postgres

import (
	"context"

	"example.com/tracefix/store"
)

type DB struct{}

func New() *DB { return &DB{} }

func (d *DB) Get(ctx context.Context, key string) (string, error) { return "", store.ErrMissing }
func (d *DB) Put(ctx context.Context, key, value string) error    { return nil }
func (d *DB) Close() error                                        { return nil }
`)
	runGoModTidy(t, dir)
	err := runGenerate(cfg(dir))
	if err == nil {
		t.Fatal("generate succeeded with no trace API in the build")
	}
	if !strings.Contains(err.Error(), "needs go.opentelemetry.io/otel/trace, which nothing in this module's build imports") {
		t.Errorf("diagnostic:\n%v", err)
	}
}
