package load

import (
	"strings"
	"testing"
)

// TestFindSpecRejectsBindToAnotherInterface covers servo.Bind[I, C]() where
// C is itself an interface: Bind resolves its second type argument via an
// exact-type lookup, which bypasses structural interface search entirely,
// so binding to another interface doesn't chain into that interface's own
// implementations — it just fails with a bare "no provider" and no
// candidates, worse than not declaring a Bind at all. Caught at parse time
// instead.
func TestFindSpecRejectsBindToAnotherInterface(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/bindiface\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\ntype Other interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/bindiface/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Bind[store.Store, store.Other](),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "must be a concrete type, not an interface") {
		t.Fatalf("got err=%v, want a 'must be a concrete type, not an interface' error", err)
	}
}

// TestFindSpecRejectsOverrideToAnyType covers the same rule applied to
// Override, and specifically to the empty interface (any), which is
// equally never a valid concrete provider result.
func TestFindSpecRejectsOverrideToAnyType(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/overrideany\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/overrideany/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Override[store.Store, any](),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "must be a concrete type, not an interface") {
		t.Fatalf("got err=%v, want a 'must be a concrete type, not an interface' error", err)
	}
}

func TestFindSpecRejectsDuplicateBindForSameInterface(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/dupbind\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "a/a.go", "package a\n\ntype A struct{}\n\nfunc (x *A) Get(key string) string { return \"\" }\n\nfunc New() *A { return &A{} }\n")
	mustWriteFile(t, dir, "b/b.go", "package b\n\ntype B struct{}\n\nfunc (x *B) Get(key string) string { return \"\" }\n\nfunc New() *B { return &B{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/dupbind/a"
	"example.com/dupbind/b"
	"example.com/dupbind/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Bind[store.Store, *a.A](),
		servo.Bind[store.Store, *b.B](),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("got err=%v, want a 'declared twice' error instead of silently picking the second Bind", err)
	}
}

func TestFindSpecRejectsDuplicateOverrideForSameInterface(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/dupoverride\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "a/a.go", "package a\n\ntype A struct{}\n\nfunc (x *A) Get(key string) string { return \"\" }\n\nfunc New() *A { return &A{} }\n")
	mustWriteFile(t, dir, "b/b.go", "package b\n\ntype B struct{}\n\nfunc (x *B) Get(key string) string { return \"\" }\n\nfunc New() *B { return &B{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/dupoverride/a"
	"example.com/dupoverride/b"
	"example.com/dupoverride/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Bind[store.Store, *a.A](),
		servo.Override[store.Store, *a.A](),
		servo.Override[store.Store, *b.B](),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("got err=%v, want a 'declared twice' error instead of silently picking the second Override", err)
	}
}

// TestFindSpecDoesNotRecognizeDotImportedBuildCall documents current,
// deliberately-unsupported behavior rather than fixing anything:
// resolveCalledFunc/markerCall only recognize marker calls written as a
// selector (servo.Build, servo.Root[T]()). A dot-imported spec
// (`import . "github.com/okian/servo/v3/servo"`, then a bare `Build(...)`
// call) parses call.Fun as a plain *ast.Ident, not a *ast.SelectorExpr, so
// it is invisible to the scan. The failure mode is a clear "no
// servo.Build(...) call found" — the same message as a spec file with no
// Build call at all — not a crash, a silent no-op, or a confusing
// half-resolved graph, so this is an acceptable, documented limitation
// rather than a bug: dot-importing is already a discouraged Go pattern, and
// supporting it would mean recognizing marker calls in two different
// syntactic shapes throughout resolveCalledFunc/markerCall.
func TestFindSpecDoesNotRecognizeDotImportedBuildCall(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/dotimport\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/dotimport/api"
	. "github.com/okian/servo/v3/servo"
)

func Wire() {
	Build(
		Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "no servo.Build") {
		t.Fatalf("got err=%v, want the same 'no servo.Build' error as no Build call at all", err)
	}
}

func TestFindSpecRejectsNonCallBuildArgument(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/badarg\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v3/servo"

func Wire() {
	servo.Build(nil)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "is not a marker call") {
		t.Fatalf("got err=%v, want a 'is not a marker call' error", err)
	}
}

func TestFindSpecRejectsMarkerCallWithoutTypeArguments(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/notype\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v3/servo"

func helper() servo.Marker { return servo.Marker{} }

func Wire() {
	servo.Build(helper())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "explicit type arguments") {
		t.Fatalf("got err=%v, want an 'explicit type arguments' error", err)
	}
}

// TestFindSpecRejectsMarkerCallWithWrongArity covers markerCall's
// Instances lookup failing: go/types refuses to record instantiation info
// for a generic call whose explicit type-argument count doesn't match the
// function's type-parameter count (servo.Bind[I, C] takes exactly two), so
// the call resolves to the function but not to a specific instantiation —
// the same "must be instantiated with explicit type arguments" diagnostic
// as omitting type arguments entirely.
func TestFindSpecRejectsMarkerCallWithWrongArity(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/wrongarity\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/wrongarity/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(servo.Bind[store.Store]())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "must be instantiated with explicit type arguments") {
		t.Fatalf("got err=%v, want an 'explicit type arguments' error", err)
	}
}

func TestFindSpecRejectsUnqualifiedGenericMarkerShape(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/unqualified\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v3/servo"

func Ident[T any]() T {
	var zero T
	return zero
}

func Wire() {
	servo.Build(Ident[int]())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "unsupported marker call shape") {
		t.Fatalf("got err=%v, want an 'unsupported marker call shape' error", err)
	}
}

// TestFindSpecRejectsUnqualifiedMultiArgGenericMarkerShape is the
// IndexListExpr (2+ type argument) sibling of the IndexExpr case above:
// same unqualified-identifier shape, different AST node type.
func TestFindSpecRejectsUnqualifiedMultiArgGenericMarkerShape(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/unqualified2\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v3/servo"

func Ident2[A, B any]() A {
	var zero A
	return zero
}

func Wire() {
	servo.Build(Ident2[int, string]())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "unsupported marker call shape") {
		t.Fatalf("got err=%v, want an 'unsupported marker call shape' error", err)
	}
}

// TestFindSpecStopsAtFirstMalformedBuildCall covers specsInFile's
// walkErr guard: once the first servo.Build(...) call in a file fails to
// parse, the walk must not keep processing a second one it encounters
// afterward — the first error is reported, and the second is unexamined.
func TestFindSpecStopsAtFirstMalformedBuildCall(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/twobuilds\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v3/servo"

func WireA() {
	servo.Build(nil)
}

func WireB() {
	servo.Build(nil)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "is not a marker call") {
		t.Fatalf("got err=%v, want a 'is not a marker call' error", err)
	}
}

func TestFindSpecRejectsForeignGenericFunction(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/foreign\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "other/other.go", `package other

func Ident[T any]() T {
	var zero T
	return zero
}
`)
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/foreign/other"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(other.Ident[int]())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "not a servo marker call") {
		t.Fatalf("got err=%v, want a 'not a servo marker call' error", err)
	}
}

// TestFindSpecAcceptsHTTPMarker covers servo.HTTP(): a plain, no-type-param
// marker that opts this injector into the generated HTTP server. The spec
// records only that it was declared and where — routes are discovered
// module-wide by a separate scan.
func TestFindSpecAcceptsHTTPMarker(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/httpmarker\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.HTTP(),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if spec.HTTP == nil {
		t.Fatalf("Spec.HTTP is nil, want the HTTP declaration recorded")
	}
	if spec.HTTP.Pos.Line == 0 || !strings.HasSuffix(spec.HTTP.Pos.Filename, "spec.go") {
		t.Fatalf("Spec.HTTP.Pos = %v, want the marker's position in spec.go", spec.HTTP.Pos)
	}
}

// TestFindSpecRejectsDuplicateHTTPMarker: one spec gets one server, so a
// second servo.HTTP() is reported against the first rather than silently
// collapsed.
func TestFindSpecRejectsDuplicateHTTPMarker(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/duphttp\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.HTTP(),
		servo.HTTP(),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "servo.HTTP() declared twice") {
		t.Fatalf("got err=%v, want a 'servo.HTTP() declared twice' error", err)
	}
}

// httpOptsModule materializes a module with a mw package (middleware and an
// extractor for the markers to name) and the given spec body, for the
// HTTP-options parse tests.
func httpOptsModule(t *testing.T, specBody string) *Loaded {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/httpopts\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "mw/mw.go", `package mw

import "net/http"

type User struct{ Name string }

type Recover struct{}

func NewRecover() *Recover { return &Recover{} }

func (m *Recover) Middleware(next http.Handler) http.Handler { return next }

type Auth struct{}

func NewAuth() *Auth { return &Auth{} }

func (m *Auth) Middleware(next http.Handler) http.Handler { return next }

type Audit struct{}

func NewAudit() *Audit { return &Audit{} }

func (m *Audit) Middleware(next http.Handler) http.Handler { return next }

type UserExtractor struct{}

func NewUserExtractor() *UserExtractor { return &UserExtractor{} }

func (e *UserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }
`)
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/httpopts/mw"
	"github.com/okian/servo/v3/servo"
)

var _ = mw.NewRecover

func Wire() {
`+specBody+`
}
`)
	runGoModTidy(t, dir)
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

func TestFindSpecParsesHTTPOptionsAndExtract(t *testing.T) {
	loaded := httpOptsModule(t, `	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Group("internal"),
			servo.Use[*mw.Recover](),
			servo.Use[*mw.Auth](servo.Group("internal")),
			servo.Use[*mw.Audit](servo.Route("POST /order/{category}/")),
		),
		servo.Extract[*mw.UserExtractor](),
	)`)
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if spec.HTTP == nil {
		t.Fatalf("Spec.HTTP is nil")
	}
	if len(spec.HTTP.Groups) != 2 || spec.HTTP.Groups[0].Name != "telemetry" || spec.HTTP.Groups[1].Name != "internal" {
		t.Fatalf("Groups = %+v", spec.HTTP.Groups)
	}
	uses := spec.HTTP.Uses
	if len(uses) != 3 {
		t.Fatalf("Uses = %+v", uses)
	}
	if uses[0].Type.String() != "*example.com/httpopts/mw.Recover" || len(uses[0].Groups) != 0 || len(uses[0].Routes) != 0 {
		t.Errorf("Uses[0] = %+v", uses[0])
	}
	if uses[1].Type.String() != "*example.com/httpopts/mw.Auth" || len(uses[1].Groups) != 1 || uses[1].Groups[0] != "internal" {
		t.Errorf("Uses[1] = %+v", uses[1])
	}
	if uses[2].Type.String() != "*example.com/httpopts/mw.Audit" || len(uses[2].Routes) != 1 || uses[2].Routes[0].Pattern != "POST /order/{category}/" {
		t.Errorf("Uses[2] = %+v", uses[2])
	}
	if len(spec.Extracts) != 1 || spec.Extracts[0].Type.String() != "*example.com/httpopts/mw.UserExtractor" {
		t.Fatalf("Extracts = %+v", spec.Extracts)
	}
	if spec.Extracts[0].Pos.Line == 0 {
		t.Fatalf("Extract position missing")
	}
}

// Every malformed option is a parse-time error with a position — the same
// contract every other marker already has.
func TestFindSpecRejectsBadHTTPOptions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "Group at Build top level",
			body: `	servo.Build(servo.HTTP(), servo.Group("x"))`,
			want: "servo.Group belongs inside servo.HTTP(...)",
		},
		{
			name: "Use at Build top level",
			body: `	servo.Build(servo.HTTP(), servo.Use[*mw.Recover]())`,
			want: "servo.Use belongs inside servo.HTTP(...)",
		},
		{
			name: "Route at Build top level",
			body: `	servo.Build(servo.HTTP(), servo.Route("GET /x"))`,
			want: "servo.Route belongs inside servo.Use(...)",
		},
		{
			name: "Route directly inside HTTP",
			body: `	servo.Build(servo.HTTP(servo.Route("GET /x")))`,
			want: "servo.Route belongs inside servo.Use(...)",
		},
		{
			name: "declaring the default group",
			body: `	servo.Build(servo.HTTP(servo.Group("default")))`,
			want: `"default" is the implicit group`,
		},
		{
			name: "bad group name",
			body: `	servo.Build(servo.HTTP(servo.Group("bad name")))`,
			want: "must match [A-Za-z0-9_-]+",
		},
		{
			name: "duplicate group",
			body: `	servo.Build(servo.HTTP(servo.Group("x"), servo.Group("x")))`,
			want: `servo.Group("x") declared twice`,
		},
		{
			name: "Use mixing selector kinds",
			body: `	servo.Build(servo.HTTP(servo.Use[*mw.Auth](servo.Group("x"), servo.Route("GET /x"))))`,
			want: "selects groups or routes, not both",
		},
		{
			name: "non-constant group name",
			body: `	name := "x"
	servo.Build(servo.HTTP(servo.Group(name)))`,
			want: "must be a constant string",
		},
		{
			name: "duplicate Extract",
			body: `	servo.Build(servo.HTTP(), servo.Extract[*mw.UserExtractor](), servo.Extract[*mw.UserExtractor]())`,
			want: "servo.Extract[*example.com/httpopts/mw.UserExtractor] declared twice",
		},
		{
			name: "non-option inside HTTP",
			body: `	servo.Build(servo.HTTP(servo.Root[*mw.Recover]()))`,
			want: "servo.Root is not a servo.HTTP option",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loaded := httpOptsModule(t, c.body)
			_, err := FindSpec(loaded)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got err=%v, want a %q error", err, c.want)
			}
		})
	}
}

// TestFindSpecRejectsAServoFunctionThatIsNotABuildMarker covers
// parseMarkerArgs' final branch. The servo package exports functions that
// are not Build markers — the report helpers, the scope-window
// calculation — and one of them written into a Build call resolves exactly
// as a marker does: same package, same call shape, so every check up to
// the switch passes. The switch has to name what it found, because the
// alternative is accepting the argument and silently resolving a graph
// that ignores it.
func TestFindSpecRejectsAServoFunctionThatIsNotABuildMarker(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/notamarker\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/notamarker/api"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.MergeNodeResults("health"),
	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "unrecognized servo marker") {
		t.Fatalf("got err=%v, want an 'unrecognized servo marker' error", err)
	}
	if !strings.Contains(err.Error(), "MergeNodeResults") {
		t.Errorf("got err=%v, want it to name the function it did not recognize", err)
	}
}
