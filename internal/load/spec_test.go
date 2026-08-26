package load

import (
	"strings"
	"testing"
)

func TestFindSpecRejectsNonCallBuildArgument(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/badarg\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v2/servo"

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
	mustWriteFile(t, dir, "go.mod", "module example.com/notype\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v2/servo"

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
	mustWriteFile(t, dir, "go.mod", "module example.com/unqualified\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v2/servo"

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
	mustWriteFile(t, dir, "go.mod", "module example.com/unqualified2\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v2/servo"

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
	mustWriteFile(t, dir, "go.mod", "module example.com/twobuilds\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import "github.com/okian/servo/v2/servo"

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
	mustWriteFile(t, dir, "go.mod", "module example.com/foreign\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
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
	"github.com/okian/servo/v2/servo"
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
