package load

import (
	"path/filepath"
	"strings"
	"testing"
)

// writeMultiInjectorModule materializes a module with two independent
// injectors, each its own package main under cmd/ — the realistic
// monorepo-with-several-binaries shape.
func writeMultiInjectorModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/multiapp

go 1.23

require github.com/okian/servo/v2 v2.0.0

replace github.com/okian/servo/v2 => `+root+`
`)

	write("api/api.go", `package api

type Server struct{}

func New() *Server { return &Server{} }
`)

	write("worker/worker.go", `package worker

type Consumer struct{}

func New() *Consumer { return &Consumer{} }
`)

	write("cmd/apisvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multiapp/api"
	"github.com/okian/servo/v2/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
`)

	write("cmd/workersvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/multiapp/worker"
	"github.com/okian/servo/v2/servo"
)

func wire() {
	servo.Build(
		servo.Root[*worker.Consumer](),
	)
}
`)

	runGoModTidy(t, dir)
	return dir
}

func TestFindSpecsDiscoversMultipleInjectorsInDifferentPackages(t *testing.T) {
	dir := writeMultiInjectorModule(t)
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	specs, err := FindSpecs(loaded)
	if err != nil {
		t.Fatalf("FindSpecs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2", len(specs))
	}
	// Sorted by injector package path, so this is order-independent of
	// discovery/walk order.
	if !strings.Contains(specs[0].InjectorPkg.PkgPath, "apisvc") {
		t.Errorf("specs[0] = %s, want the apisvc injector first", specs[0].InjectorPkg.PkgPath)
	}
	if !strings.Contains(specs[1].InjectorPkg.PkgPath, "workersvc") {
		t.Errorf("specs[1] = %s, want the workersvc injector second", specs[1].InjectorPkg.PkgPath)
	}
}

func TestFindSpecAsksToDisambiguateWhenMultipleFound(t *testing.T) {
	dir := writeMultiInjectorModule(t)
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpec(loaded)
	if err == nil || !strings.Contains(err.Error(), "pass --dir") {
		t.Fatalf("got err=%v, want a 'pass --dir' disambiguation error", err)
	}
}

func TestFindSpecScopedToOneInjectorDirectorySucceeds(t *testing.T) {
	dir := writeMultiInjectorModule(t)
	// Scoping the load to one injector's own directory is the workaround
	// this test documents: package main can never import another package
	// main, so cmd/workersvc is simply unreachable from cmd/apisvc's own
	// transitive closure regardless of directory layout.
	loaded, err := Load(Config{Dir: filepath.Join(dir, "cmd", "apisvc")})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec scoped to cmd/apisvc: %v", err)
	}
	if !strings.Contains(spec.InjectorPkg.PkgPath, "apisvc") {
		t.Errorf("got injector %s, want apisvc", spec.InjectorPkg.PkgPath)
	}
}

func TestFindSpecsErrorsOnSamePackageAmbiguity(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/dupe\n\ngo 1.23\n\nrequire github.com/okian/servo/v2 v2.0.0\n\nreplace github.com/okian/servo/v2 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	// Two Build calls in the SAME package (different files) is genuinely
	// ambiguous — unlike two different packages, that package can only
	// ever have one generated file.
	mustWriteFile(t, dir, "spec/a.go", `//go:build servoinject

package spec

import (
	"example.com/dupe/api"
	"github.com/okian/servo/v2/servo"
)

func WireA() {
	servo.Build(servo.Root[*api.Server]())
}
`)
	mustWriteFile(t, dir, "spec/b.go", `//go:build servoinject

package spec

import (
	"example.com/dupe/api"
	"github.com/okian/servo/v2/servo"
)

func WireB() {
	servo.Build(servo.Root[*api.Server]())
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err = FindSpecs(loaded)
	if err == nil || !strings.Contains(err.Error(), "same package") {
		t.Fatalf("got err=%v, want a same-package ambiguity error", err)
	}
}
