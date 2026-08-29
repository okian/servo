package load

import (
	"path/filepath"
	"strings"
	"testing"
)

// includeModulePath is the module path every fixture in this file uses. It
// is a constant rather than a per-test name because these fixtures are
// written as literal Go source and have to import their own packages by
// name; each test still gets its own t.TempDir, so nothing is shared.
const includeModulePath = "example.com/inc"

// writeIncludeModule materializes the module the servo.Value and
// servo.Include cases need — an interface with two implementations, a root
// that depends on the interface, and a type nothing in the graph can build
// — plus whatever spec and shared-set files the case supplies itself,
// keyed by path relative to the module root.
//
// It is built from the same primitives writeFixtureModule uses rather than
// from writeFixtureModule itself: these cases vary the spec *file* — its
// imports, and which package the marker set it splices lives in — which an
// extra-arguments hook into one fixed spec file cannot express.
func writeIncludeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", "module "+includeModulePath+`

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+repoRoot(t)+"\n")

	write("store/store.go", `package store

type Store interface{ Get(key string) string }
`)
	write("memory/memory.go", `package memory

type Mem struct{}

func (m *Mem) Get(key string) string { return "" }

func New() *Mem { return &Mem{} }
`)
	write("redis/redis.go", `package redis

type Redis struct{}

func (r *Redis) Get(key string) string { return "" }

func New() *Redis { return &Redis{} }
`)
	write("api/api.go", `package api

import "example.com/inc/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	write("conf/conf.go", `package conf

// Flags is parsed in main, so nothing in the graph can build it — the case
// servo.Value exists for.
type Flags struct{ DSN string }
`)

	for rel, content := range files {
		write(rel, content)
	}
	runGoModTidy(t, dir)
	return dir
}

// TestFindSpecCollectsValueDeclarations covers parseMarkerArgs' Value
// branch. A declared value is the one node in the graph no provider
// builds, so it has to arrive as its own list: folded in with the Roots it
// would be something the injector tries to construct, and dropped
// entirely the generated NewWith would have no field to fill.
func TestFindSpecCollectsValueDeclarations(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/conf"
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Value[conf.Flags](),
		servo.Bind[store.Store, *memory.Mem](),
	)
}
`,
	})

	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Values) != 1 {
		t.Fatalf("got %d values, want 1", len(spec.Values))
	}
	if want := includeModulePath + "/conf.Flags"; spec.Values[0].Key.String() != want {
		t.Errorf("value key = %s, want %s", spec.Values[0].Key.String(), want)
	}
	// A Value is supplied, not built: counting it as a root as well would
	// make the injector try to construct the very type the marker says it
	// cannot.
	if len(spec.Roots) != 1 {
		t.Errorf("got %d roots, want 1 — servo.Value must not also register as a root", len(spec.Roots))
	}
	if len(spec.Binds) != 1 {
		t.Errorf("got %d binds, want 1", len(spec.Binds))
	}
}

// TestFindSpecRejectsDuplicateValueForSameType: two Values for one type
// would emit the same field into the generated Values struct twice, which
// does not compile — and the author who wrote the second one meant
// something (a second, differently-named value) that servo cannot express.
// Saying so at parse time beats a compile error inside generated code.
func TestFindSpecRejectsDuplicateValueForSameType(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/conf"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Value[conf.Flags](),
		servo.Value[conf.Flags](),
	)
}
`,
	})

	_, err := findSpecIn(t, dir)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("got err=%v, want a 'declared twice' error naming the duplicate servo.Value", err)
	}
	if !strings.Contains(err.Error(), "conf.Flags") {
		t.Errorf("got err=%v, want it to name the duplicated type", err)
	}
}

// TestFindSpecSplicesIncludeFromAnotherPackage is the case servo.Include
// exists for: a marker set that lives in its own package so several
// injectors can share it. It is also the only shape that exercises
// findFuncDecl/allPackages for real — the declaration is not in the
// injector's own package, so it can only be found by walking the import
// graph.
func TestFindSpecSplicesIncludeFromAnotherPackage(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"wiring/wiring.go": `//go:build servoinject

package wiring

import (
	"example.com/inc/conf"
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Value[conf.Flags](),
		servo.Bind[store.Store, *memory.Mem](),
	}
}
`,
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`,
	})

	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Binds) != 1 {
		t.Fatalf("got %d binds, want the one the shared set declares", len(spec.Binds))
	}
	bind := spec.Binds[0]
	if want := "*" + includeModulePath + "/memory.Mem"; bind.Concrete.String() != want {
		t.Errorf("bind concrete = %s, want %s", bind.Concrete.String(), want)
	}
	// Included is what lets a local Bind supersede this one later, so it
	// has to be set on everything a splice brings in.
	if !bind.Included {
		t.Error("spliced bind is not marked Included — a local Bind could then never supersede it")
	}
	// The position must point into the shared set's own file, not at the
	// Include call: every later diagnostic about this Bind sends the
	// author to the line they would have to edit.
	if got := filepath.Base(bind.Pos.Filename); got != "wiring.go" {
		t.Errorf("bind position = %s, want it inside the shared set's own file", bind.Pos)
	}
	if len(spec.Values) != 1 {
		t.Fatalf("got %d values, want the one the shared set declares", len(spec.Values))
	}
	if len(spec.Roots) != 1 {
		t.Errorf("got %d roots, want the injector's own", len(spec.Roots))
	}
}

// TestFindSpecSplicesIncludeFromTheSamePackage covers includedFunc's plain
// identifier branch (an unqualified name, where the cross-package case is
// a selector) and fileOf's search: the shared set is in a second file of
// the injector's own package, so the file the build-tag check runs against
// is only the right one if fileOf actually matches the declaration to its
// file rather than taking the package's first.
func TestFindSpecSplicesIncludeFromTheSamePackage(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"spec/shared.go": `//go:build servoinject

package spec

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
	}
}
`,
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(shared),
		servo.Root[*api.Server](),
	)
}
`,
	})

	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Binds) != 1 {
		t.Fatalf("got %d binds, want the one the shared set declares", len(spec.Binds))
	}
	if got := filepath.Base(spec.Binds[0].Pos.Filename); got != "shared.go" {
		t.Errorf("bind position = %s, want it inside shared.go", spec.Binds[0].Pos)
	}
}

// TestFindSpecRejectsIncludeOfUntaggedMarkerSet: every marker panics if it
// is ever executed, so a shared set is exactly as dangerous in the real
// binary as a spec file is. Without the build tag it compiles into the
// program — and the refusal has to happen here, because nothing else would
// notice until the panic.
func TestFindSpecRejectsIncludeOfUntaggedMarkerSet(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"wiring/wiring.go": `package wiring

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
	}
}
`,
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`,
	})

	_, err := findSpecIn(t, dir)
	if err == nil || !strings.Contains(err.Error(), "without a `//go:build servoinject` constraint") {
		t.Fatalf("got err=%v, want a refusal naming the missing build constraint", err)
	}
	if !strings.Contains(err.Error(), "wiring.Shared") {
		t.Errorf("got err=%v, want it to name the offending marker set", err)
	}
}

// TestFindSpecRejectsMarkerSetItWouldHaveToExecute covers
// markerSliceLiteral. An included function is read as syntax and never
// called, exactly like Build's own argument list — so every body below is
// one servo would have to *run* to learn what markers it returns, and the
// only honest answer is to refuse. Each is a shape someone will
// reasonably try, so each gets the diagnostic rather than a nil
// dereference or a silently empty marker set.
func TestFindSpecRejectsMarkerSetItWouldHaveToExecute(t *testing.T) {
	const specFile = `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`
	const header = `//go:build servoinject

package wiring

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

`
	cases := []struct {
		name    string
		decl    string
		wantErr string
	}{
		{
			// The obvious refactor once a shared set grows: name the
			// markers, then return them. servo would have to evaluate the
			// assignment to know what is in the slice.
			name: "a local variable holding the markers",
			decl: `func Shared() []servo.Marker {
	bind := servo.Bind[store.Store, *memory.Mem]()
	return []servo.Marker{bind}
}`,
			wantErr: "must be exactly",
		},
		{
			name: "a package-level variable returned by name",
			decl: `var markers = []servo.Marker{servo.Bind[store.Store, *memory.Mem]()}

func Shared() []servo.Marker { return markers }`,
			wantErr: "must be exactly",
		},
		{
			// Named result plus a bare return: one statement, but no
			// expression at all to read the markers out of.
			name: "a bare return with a named result",
			decl: `func Shared() (markers []servo.Marker) {
	return
}`,
			wantErr: "must be exactly",
		},
		{
			// A stub someone left behind. Refusing it is the difference
			// between a diagnostic and an injector silently missing every
			// binding the set was supposed to carry.
			name:    "a body that does not return at all",
			decl:    `func Shared() []servo.Marker { panic("not wired yet") }`,
			wantErr: "must be exactly",
		},
		{
			// A declaration with no body at all (the shape a function
			// implemented in assembly has). decl.Body is nil here, so this
			// is the case that would be a nil dereference rather than a
			// diagnostic if the guard were dropped.
			name:    "a declaration with no body",
			decl:    `func Shared() []servo.Marker`,
			wantErr: "must be exactly",
		},
		{
			// The slice literal is there, but one element is a bare
			// servo.Marker value rather than a marker call. An included
			// set is held to the same rule as Build's own arguments, or it
			// would be a hole in the check.
			name: "a literal element that is not a marker call",
			decl: `func Shared() []servo.Marker {
	return []servo.Marker{servo.Marker{}}
}`,
			wantErr: "is not a marker call",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeIncludeModule(t, map[string]string{
				"wiring/wiring.go": header + c.decl + "\n",
				"spec/spec.go":     specFile,
			})
			_, err := findSpecIn(t, dir)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got err=%v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestFindSpecRejectsIncludeArgumentThatIsNotADeclaredFunction covers
// includedFunc. Include's argument is a name servo resolves to a
// declaration it then reads; anything whose value only exists at runtime —
// a literal written in place, a method bound to a receiver, the result of
// a call, a variable someone could reassign — has no declaration to read,
// and the type checker cannot tell the difference because all four have
// exactly the right Go type.
func TestFindSpecRejectsIncludeArgumentThatIsNotADeclaredFunction(t *testing.T) {
	const header = `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"github.com/okian/servo/v3/servo"
)

`
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "a function literal written in place",
			src: `func Wire() {
	servo.Build(
		servo.Include(func() []servo.Marker { return nil }),
		servo.Root[*api.Server](),
	)
}`,
		},
		{
			name: "a method value",
			src: `type sets struct{}

func (sets) Shared() []servo.Marker { return nil }

var s sets

func Wire() {
	servo.Build(
		servo.Include(s.Shared),
		servo.Root[*api.Server](),
	)
}`,
		},
		{
			name: "the result of a call",
			src: `func pick() func() []servo.Marker { return nil }

func Wire() {
	servo.Build(
		servo.Include(pick()),
		servo.Root[*api.Server](),
	)
}`,
		},
		{
			name: "a variable of function type",
			src: `var shared = func() []servo.Marker { return nil }

func Wire() {
	servo.Build(
		servo.Include(shared),
		servo.Root[*api.Server](),
	)
}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeIncludeModule(t, map[string]string{"spec/spec.go": header + c.src + "\n"})
			_, err := findSpecIn(t, dir)
			if err == nil || !strings.Contains(err.Error(), "must name a declared func() []servo.Marker") {
				t.Fatalf("got err=%v, want a 'must name a declared func' error", err)
			}
		})
	}
}

// TestFindSpecRejectsIncludeWithWrongArgumentCount covers spliceInclude's
// arity guard. Both shapes below are compile errors the go command would
// report eventually, but go/packages deliberately loads a package with
// type errors (an injector legitimately has them before its first
// generate), so spliceInclude reaches an argument list of the wrong length
// and must not index into it blindly.
func TestFindSpecRejectsIncludeWithWrongArgumentCount(t *testing.T) {
	const header = `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"github.com/okian/servo/v3/servo"
)

func sharedA() []servo.Marker { return nil }

func sharedB() []servo.Marker { return nil }

`
	cases := []struct {
		name string
		call string
	}{
		{name: "no argument at all", call: "servo.Include()"},
		{name: "two marker sets in one Include", call: "servo.Include(sharedA, sharedB)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := header + `func Wire() {
	servo.Build(
		` + c.call + `,
		servo.Root[*api.Server](),
	)
}
`
			dir := writeIncludeModule(t, map[string]string{"spec/spec.go": src})
			_, err := findSpecIn(t, dir)
			if err == nil || !strings.Contains(err.Error(), "takes exactly one argument") {
				t.Fatalf("got err=%v, want a 'takes exactly one argument' error", err)
			}
		})
	}
}

// TestFindSpecSplicesNestedIncludes: an included set may itself Include
// another, and the inner name is resolved in the *including* package's
// type information rather than the injector's — base.Base is not even
// imported by the spec file, so nothing else could resolve it.
func TestFindSpecSplicesNestedIncludes(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"base/base.go": `//go:build servoinject

package base

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Base() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
	}
}
`,
		"wiring/wiring.go": `//go:build servoinject

package wiring

import (
	"example.com/inc/base"
	"example.com/inc/conf"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Include(base.Base),
		servo.Value[conf.Flags](),
	}
}
`,
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`,
	})

	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Binds) != 1 {
		t.Fatalf("got %d binds, want the one the innermost set declares", len(spec.Binds))
	}
	if got := filepath.Base(spec.Binds[0].Pos.Filename); got != "base.go" {
		t.Errorf("bind position = %s, want it inside the nested set's own file", spec.Binds[0].Pos)
	}
	if len(spec.Values) != 1 {
		t.Errorf("got %d values, want the one the outer set declares — a nested Include must not lose its sibling markers", len(spec.Values))
	}
}

// TestFindSpecReportsIncludeCycleWithThePath: two shared sets that include
// each other are a program with no fixed point, and following them naively
// is an infinite recursion that ends as a stack overflow with no file name
// in it. The chain is carried through the splice precisely so the
// diagnostic can name every function on the path back to the one that
// closed it.
//
// Both cycles below are within one package because that is the only place
// one can exist: two packages that included each other would be an import
// cycle, which the go command rejects first.
func TestFindSpecReportsIncludeCycleWithThePath(t *testing.T) {
	const specFile = `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`
	const header = `//go:build servoinject

package wiring

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

`
	cases := []struct {
		name  string
		decls string
		want  []string
	}{
		{
			name: "a set that includes itself",
			decls: `func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
		servo.Include(Shared),
	}
}`,
			want: []string{"cycle", "wiring.Shared"},
		},
		{
			name: "a two-step cycle names both sets on the path",
			decls: `func Shared() []servo.Marker {
	return []servo.Marker{servo.Include(other)}
}

func other() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
		servo.Include(Shared),
	}
}`,
			want: []string{"cycle", "wiring.Shared", "wiring.other"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeIncludeModule(t, map[string]string{
				"wiring/wiring.go": header + c.decls + "\n",
				"spec/spec.go":     specFile,
			})
			_, err := findSpecIn(t, dir)
			if err == nil {
				t.Fatal("expected a cycle diagnostic, got a spec")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("got err=%v, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestLocalDeclarationSupersedesAnIncludedOne is the rule that makes a
// shared set worth having: a set several injectors share is only reusable
// if one of them can differ from it, so the local file has the last word.
// Both lists get the same treatment, and they get it independently — a
// local Bind must not disturb the included Override, which is the
// documented way to say "this one, except under servotest".
func TestLocalDeclarationSupersedesAnIncludedOne(t *testing.T) {
	const wiringFile = `//go:build servoinject

package wiring

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
		servo.Override[store.Store, *memory.Mem](),
	}
}
`
	cases := []struct {
		name string
		// marker is the one the injector re-declares locally; the other
		// list must come through the splice untouched.
		marker     string
		local      func(*Spec) []BindDecl
		unaffected func(*Spec) []BindDecl
	}{
		{
			name:       "Bind",
			marker:     "Bind",
			local:      func(s *Spec) []BindDecl { return s.Binds },
			unaffected: func(s *Spec) []BindDecl { return s.Overrides },
		},
		{
			name:       "Override",
			marker:     "Override",
			local:      func(s *Spec) []BindDecl { return s.Overrides },
			unaffected: func(s *Spec) []BindDecl { return s.Binds },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, err := findSpecIn(t, writeIncludeModule(t, map[string]string{
				"wiring/wiring.go": wiringFile,
				"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/redis"
	"example.com/inc/store"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
		servo.` + c.marker + `[store.Store, *redis.Redis](),
	)
}
`,
			}))
			if err != nil {
				t.Fatalf("FindSpec: %v", err)
			}

			local := c.local(spec)
			if len(local) != 1 {
				t.Fatalf("got %d %s declarations, want the local one to have replaced the included one rather than joined it", len(local), c.marker)
			}
			if want := "*" + includeModulePath + "/redis.Redis"; local[0].Concrete.String() != want {
				t.Errorf("%s concrete = %s, want %s — the local file must win", c.marker, local[0].Concrete.String(), want)
			}
			if local[0].Included {
				t.Errorf("%s is still marked Included after being superseded by a local declaration", c.marker)
			}

			other := c.unaffected(spec)
			if len(other) != 1 || other[0].Concrete.String() != "*"+includeModulePath+"/memory.Mem" {
				t.Errorf("got %v for the other list, want the included declaration untouched", other)
			}
		})
	}
}

// TestFindSpecRejectsALocalBindWrittenBeforeTheIncludeItOverrides pins the
// ordering half of the rule above, which is not symmetric and cannot be:
// an Include splices its markers in *where the Include sits*, so a local
// Bind above it is not an override of the shared one — the shared one
// arrives second and collides with it. That is a real ambiguity (nothing
// in the file says which was meant to win), so it gets the same "declared
// twice" diagnostic two local Binds would, rather than silently resolving
// one way or the other.
func TestFindSpecRejectsALocalBindWrittenBeforeTheIncludeItOverrides(t *testing.T) {
	dir := writeIncludeModule(t, map[string]string{
		"wiring/wiring.go": `//go:build servoinject

package wiring

import (
	"example.com/inc/memory"
	"example.com/inc/store"
	"github.com/okian/servo/v3/servo"
)

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *memory.Mem](),
	}
}
`,
		"spec/spec.go": `//go:build servoinject

package spec

import (
	"example.com/inc/api"
	"example.com/inc/redis"
	"example.com/inc/store"
	"example.com/inc/wiring"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Bind[store.Store, *redis.Redis](),
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
`,
	})

	_, err := findSpecIn(t, dir)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("got err=%v, want a 'declared twice' error: a local Bind above the Include is ambiguous, not an override", err)
	}
}
