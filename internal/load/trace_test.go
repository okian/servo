package load

import (
	"strings"
	"testing"
)

// writeTraceFixture is writeFixtureModule with the declarations the Trace
// parser's rules are about: an exported error, an unexported one, a
// non-error package-level var, an interface with an unexported method, an
// empty interface, and a concrete type.
func writeTraceFixture(t *testing.T, specArgs string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/traced

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	write("store/store.go", `package store

import (
	"context"
	"errors"
)

var ErrMissing = errors.New("store: missing")

var errHidden = errors.New("store: hidden")

var NotAnError = 42

type Store interface {
	Get(ctx context.Context, key string) (string, error)
}

type Opener interface {
	open() error
}

type option struct{}

// Reader's method is exported, but its signature names a type only this
// package can spell.
type Reader interface {
	Read(ctx context.Context, opts ...option) ([]byte, error)
}

type Empty interface{}

type Concrete struct{}

func Hidden() error { return errHidden }
`)
	write("memory/memory.go", `package memory

import "context"

type Mem struct{}

func (m *Mem) Get(ctx context.Context, key string) (string, error) { return "", nil }

func (m *Mem) open() error { return nil }

func New() *Mem { return &Mem{} }
`)
	write("api/api.go", `package api

import "example.com/traced/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	write("spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/traced/api"
	"example.com/traced/memory"
	"example.com/traced/store"
	"github.com/okian/servo/v3/servo"
)

var _ = memory.New

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
`+specArgs+`	)
}
`)
	runGoModTidy(t, dir)
	return dir
}

func findTraceSpec(t *testing.T, dir string) (*Spec, error) {
	t.Helper()
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return FindSpec(loaded)
}

func TestFindSpecParsesTraceWithExpected(t *testing.T) {
	dir := writeTraceFixture(t, `		servo.Trace[store.Store](servo.Expected(store.ErrMissing)),
`)
	spec, err := findTraceSpec(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Traces) != 1 {
		t.Fatalf("Traces = %d, want 1", len(spec.Traces))
	}
	tr := spec.Traces[0]
	if tr.Iface.String() != "example.com/traced/store.Store" {
		t.Fatalf("traced interface = %s", tr.Iface)
	}
	if tr.Pos.Line == 0 || !strings.HasSuffix(tr.Pos.Filename, "spec.go") {
		t.Fatalf("Pos = %v, want the marker's position in spec.go", tr.Pos)
	}
	if len(tr.Expected) != 1 || tr.Expected[0].Obj.Name() != "ErrMissing" || tr.Expected[0].Obj.Pkg().Path() != "example.com/traced/store" {
		t.Fatalf("Expected = %+v, want store.ErrMissing", tr.Expected)
	}
	if tr.Included {
		t.Fatal("a Trace written in the spec file itself must not be marked Included")
	}
}

func TestFindSpecRejectsBadTraceDeclarations(t *testing.T) {
	cases := []struct {
		name, args, want string
	}{
		{"concrete", "servo.Trace[store.Concrete](),", "servo.Trace's type argument must be an interface, not example.com/traced/store.Concrete"},
		{"pointer", "servo.Trace[*memory.Mem](),", "must be an interface, not *example.com/traced/memory.Mem"},
		{"empty", "servo.Trace[store.Empty](),", "the interface declares no methods"},
		{"any", "servo.Trace[any](),", "must be a named interface"},
		{"unexported method", "servo.Trace[store.Opener](),", "method open is unexported, and the wrapper is generated into package example.com/traced/spec"},
		{"unexported type in signature", "servo.Trace[store.Reader](),", "method Read's signature uses example.com/traced/store.option, an unexported type the wrapper generated into package example.com/traced/spec cannot name"},
		{"twice", "servo.Trace[store.Store](), servo.Trace[store.Store](),", "servo.Trace[example.com/traced/store.Store] declared twice"},
		{"wrong option", "servo.Trace[store.Store](servo.Linger(0)),", "servo.Linger is not a servo.Trace option"},
		{"non-call option", "servo.Trace[store.Store](servo.TraceOption{}),", "servo.Trace option is not a marker call"},
		{"expected at top level", "servo.Expected(store.ErrMissing),", "servo.Expected belongs inside servo.Trace[I](...)"},
		{"expected literal", "servo.Trace[store.Store](servo.Expected(nil)),", "must name a package-level error variable or constant"},
		{"expected call", "servo.Trace[store.Store](servo.Expected(store.Hidden())),", "must name a package-level error variable or constant"},
		{"expected non-error", "servo.Trace[store.Store](servo.Expected(store.NotAnError)),", "NotAnError is a int, not an error"},
		{"expected twice", "servo.Trace[store.Store](servo.Expected(store.ErrMissing), servo.Expected(store.ErrMissing)),", "servo.Expected(ErrMissing) declared twice"},
		{"expected arity", "servo.Trace[store.Store](servo.Expected()),", "servo.Expected takes exactly one argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTraceFixture(t, "\t\t"+tc.args+"\n")
			_, err := findTraceSpec(t, dir)
			if err == nil {
				t.Fatalf("FindSpec accepted %s", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("FindSpec error = %v\nwant it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "spec.go:") {
				t.Fatalf("error does not carry the spec position: %v", err)
			}
		})
	}
}

// TestFindSpecRejectsUnexportedExpectedError: the generated errors.Is
// check lives in the injector package, so a sentinel it cannot name is
// refused at the declaration rather than as a compile error in a file the
// user is told not to read. A non-error typed non-nil literal is the
// closest the type checker lets a test get, so this one uses a real
// unexported var instead.
func TestFindSpecRejectsUnexportedExpectedError(t *testing.T) {
	dir := writeTraceFixture(t, "")
	mustWriteFile(t, dir, "spec/hidden.go", `//go:build servoinject

package spec

import "errors"

var errLocal = errors.New("local")
`)
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/traced/api"
	"example.com/traced/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Trace[store.Store](servo.Expected(errLocal)),
	)
}
`)
	// Declared in the injector package itself, an unexported sentinel is
	// fine: the generated file can name it.
	spec, err := findTraceSpec(t, dir)
	if err != nil {
		t.Fatalf("FindSpec rejected a sentinel declared in the injector package: %v", err)
	}
	if len(spec.Traces) != 1 || len(spec.Traces[0].Expected) != 1 || spec.Traces[0].Expected[0].Obj.Name() != "errLocal" {
		t.Fatalf("Traces = %+v", spec.Traces)
	}
}

// TestIncludedTraceIsSupersededByALocalOne mirrors Bind's rule: a shared
// set may trace an interface, and one injector may still redeclare it with
// its own Expected list.
func TestIncludedTraceIsSupersededByALocalOne(t *testing.T) {
	dir := writeTraceFixture(t, "")
	mustWriteFile(t, dir, "wiring/wiring.go", `//go:build servoinject

package wiring

import (
	"example.com/traced/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Trace[store.Store](),
	}
}
`)
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/traced/api"
	"example.com/traced/store"
	"example.com/traced/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
		servo.Trace[store.Store](servo.Expected(store.ErrMissing)),
	)
}
`)
	spec, err := findTraceSpec(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Traces) != 1 {
		t.Fatalf("Traces = %d, want the local declaration to replace the included one", len(spec.Traces))
	}
	if len(spec.Traces[0].Expected) != 1 || spec.Traces[0].Included {
		t.Fatalf("Traces[0] = %+v, want the local declaration with its Expected list", spec.Traces[0])
	}
}
