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
