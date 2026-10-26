package load

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// writeNarrowingModule materializes the layout that made --dir narrow the
// graph: an injector whose own imports reach one implementation of an
// interface, a second implementation nothing imports yet, a //servo:
// route in a package nothing imports, and a package outside cmd/ that
// calls nothing at all.
func writeNarrowingModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/narrow\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	write("memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	write("redis/redis.go", "package redis\n\ntype Redis struct{}\n\nfunc (r *Redis) Get(key string) string { return \"\" }\n\nfunc New() *Redis { return &Redis{} }\n")
	write("api/api.go", "package api\n\nimport \"example.com/narrow/store\"\n\ntype Server struct{ s store.Store }\n\nfunc New(s store.Store) *Server { return &Server{s: s} }\n")
	write("cmd/app/main.go", "package main\n\nimport _ \"example.com/narrow/memory\"\n\nfunc main() {}\n")
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/narrow/api"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)
	return dir
}

// TestLoadReadsTheWholeModuleWhicheverInjectorDirSelects pins Dir to what
// it is for: choosing the injectors a command processes. The load used to
// be the import closure of Dir's own packages, so `go generate` — which
// runs servo from the injector's directory — saw one implementation of
// store.Store where the module root saw two, auto-bound it, and wrote a
// file `servo check` from the root then called stale.
func TestLoadReadsTheWholeModuleWhicheverInjectorDirSelects(t *testing.T) {
	dir := writeNarrowingModule(t)

	fromInjector, err := Load(Config{Dir: filepath.Join(dir, "cmd", "app")})
	if err != nil {
		t.Fatalf("Load(cmd/app): %v", err)
	}
	fromRoot, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load(root): %v", err)
	}
	if got, want := pkgPaths(fromInjector.MainPackages()), pkgPaths(fromRoot.MainPackages()); !reflect.DeepEqual(got, want) {
		t.Errorf("main packages from cmd/app = %v\nfrom the module root = %v\nDir must not change what is loaded", got, want)
	}
	if _, ok := fromInjector.ByPath["example.com/narrow/redis"]; !ok {
		t.Error("example.com/narrow/redis is not loaded from cmd/app: an implementation nothing imports yet is still one auto-bind has to see")
	}
	if got := pkgPaths(fromInjector.Selected); !reflect.DeepEqual(got, []string{"example.com/narrow/cmd/app"}) {
		t.Errorf("Selected from cmd/app = %v, want only the injector's own package", got)
	}

	// From a directory holding no injector, the error says where they are.
	_, err = FindSpecs(mustLoad(t, Config{Dir: filepath.Join(dir, "api")}))
	if err == nil || !strings.Contains(err.Error(), "no servo.Build(...) call in") || !strings.Contains(err.Error(), filepath.Join("cmd", "app", "spec.go")) {
		t.Fatalf("FindSpecs(api) = %v, want an error pointing at cmd/app's spec", err)
	}
}

func mustLoad(t *testing.T, cfg Config) *Loaded {
	t.Helper()
	l, err := Load(cfg)
	if err != nil {
		t.Fatalf("Load(%s): %v", cfg.Dir, err)
	}
	return l
}

func pkgPaths(pkgs []*packages.Package) []string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = p.PkgPath
	}
	return out
}

// writeWorkspace materializes a go.work listing two modules: app, with the
// injector and an implementation of its own interface, and lib, a sibling
// with a second implementation, a //servo:config type and a package app
// never imports. app's go.mod replaces lib, so the module also builds with
// GOWORK=off, the way CI usually runs.
func writeWorkspace(t *testing.T) (root, app string) {
	t.Helper()
	root = t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, root, rel, content) }
	// go.work's go line has to be at least every listed module's, and
	// tidying app raises its own to whatever servo requires — so it is the
	// running toolchain's.
	write("go.work", "go "+strings.TrimPrefix(runtime.Version(), "go")+"\n\nuse (\n\t./app\n\t./lib\n)\n")
	write("lib/go.mod", "module example.com/lib\n\ngo 1.23\n")
	write("lib/cache/cache.go", "package cache\n\ntype Cache struct{}\n\nfunc (c *Cache) Get(key string) string { return \"\" }\n\nfunc New() *Cache { return &Cache{} }\n")
	write("lib/settings/settings.go", "package settings\n\n//servo:config prefix=LIB\ntype Settings struct {\n\tRegion string `config:\"region,default=eu\"`\n}\n")
	write("lib/unused/unused.go", "package unused\n\nfunc New() *int { return nil }\n")
	write("app/go.mod", "module example.com/app\n\ngo 1.23\n\nrequire (\n\texample.com/lib v0.0.0\n\tgithub.com/okian/servo/v3 v3.0.0\n)\n\nreplace (\n\texample.com/lib => ../lib\n\tgithub.com/okian/servo/v3 => "+repoRoot(t)+"\n)\n")
	write("app/store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	write("app/memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	write("app/api/api.go", "package api\n\nimport (\n\t\"example.com/app/store\"\n\t\"example.com/lib/cache\"\n\t\"example.com/lib/settings\"\n)\n\ntype Server struct{ s store.Store }\n\nfunc New(s store.Store, cfg settings.Settings) *Server { return &Server{s: s} }\n\nvar _ = cache.New\n")
	write("app/cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/app/api"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
`)
	app = filepath.Join(root, "app")
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = app
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy in app: %v\n%s", err, out)
	}
	return root, app
}

// TestScopeModuleIsTheModuleDirIsIn covers shared decision 9. In a
// workspace the go command marks every listed module Main, and servo used
// that as its own "is this the user's code" test, so a sibling module's
// implementation was auto-bound and a config companion was written into a
// sibling's directory — neither of which a GOWORK=off build, CI's usual
// one, could reproduce. --scope=module, the default, means the module Dir
// is in; --scope=workspace keeps the old reading, by request.
func TestScopeModuleIsTheModuleDirIsIn(t *testing.T) {
	_, app := writeWorkspace(t)

	module := mustLoad(t, Config{Dir: app})
	lib := module.ByPath["example.com/lib/cache"]
	if lib == nil || !lib.Module.Main {
		t.Fatalf("fixture is not exercising anything: example.com/lib/cache should be loaded with Module.Main set, got %+v", lib)
	}
	if module.InMainModule(lib) {
		t.Error("--scope=module treats a go.work sibling as the main module")
	}
	for _, p := range module.MainPackages() {
		if !strings.HasPrefix(p.PkgPath, "example.com/app") {
			t.Errorf("--scope=module main packages include %s", p.PkgPath)
		}
	}

	// The same answer CI gets without the go.work.
	t.Setenv("GOWORK", "off")
	off := mustLoad(t, Config{Dir: app})
	if got, want := pkgPaths(module.MainPackages()), pkgPaths(off.MainPackages()); !reflect.DeepEqual(got, want) {
		t.Errorf("main packages under --scope=module = %v\nunder GOWORK=off = %v", got, want)
	}
	t.Setenv("GOWORK", "")

	workspace := mustLoad(t, Config{Dir: app, Scope: ScopeWorkspace})
	if !workspace.InMainModule(workspace.ByPath["example.com/lib/cache"]) {
		t.Error("--scope=workspace leaves out a module go.work lists")
	}
	// Every listed module is loaded whole, like the main module itself —
	// a package nothing imports included.
	if _, ok := workspace.ByPath["example.com/lib/unused"]; !ok {
		t.Error("--scope=workspace did not load example.com/lib/unused: every listed module is part of the main module, all of it")
	}
}

func TestModuleScopeRejectsAnythingElse(t *testing.T) {
	for _, s := range []ModuleScope{"", ScopeModule, ScopeWorkspace} {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", s, err)
		}
	}
	if err := ModuleScope("modules").Validate(); err == nil || !strings.Contains(err.Error(), "--scope=modules") {
		t.Errorf("Validate(modules) = %v, want --scope=modules refused", err)
	}
}

// writeLibraryInjectorModule is the layout an importable injector makes:
// wiring holds the spec, and cmd/app calls the New and names the App only
// wiring's generated file declares — which the servoinject load hides, so
// cmd/app cannot type-check before, or even after, the first generation.
func writeLibraryInjectorModule(t *testing.T, extraMain string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/libinjector\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("store/store.go", "package store\n\ntype Store struct{}\n\nfunc New() *Store { return &Store{} }\n")
	write("wiring/doc.go", "// Package wiring holds the injector cmd/app calls.\npackage wiring\n")
	write("wiring/spec.go", `//go:build servoinject

package wiring

import (
	"example.com/libinjector/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*store.Store](),
	)
}
`)
	write("cmd/app/main.go", `package main

import (
	"context"

	"example.com/libinjector/wiring"
)

func run(a *wiring.App) error { return a.Run(context.Background()) }

func main() {
	app, err := wiring.New(context.Background())
	if err != nil {
		panic(err)
	}
	_ = run(app)
`+extraMain+`}
`)
	runGoModTidy(t, dir)
	return dir
}

// TestNonInjectorErrorsExcusesCallersOfAnUngeneratedInjector covers the
// one step past the injector's own package: a package that imports it and
// uses what only the generated file declares. go build passes once the file
// exists; generate, check and doctor used to fail forever, since the load
// that would write the file could not get past its caller.
func TestNonInjectorErrorsExcusesCallersOfAnUngeneratedInjector(t *testing.T) {
	dir := writeLibraryInjectorModule(t, "")
	l := mustLoad(t, Config{Dir: dir})
	caller := l.ByPath["example.com/libinjector/cmd/app"]
	if caller == nil || len(caller.Errors) == 0 {
		t.Fatal("fixture is not exercising anything: cmd/app should fail to type-check under the servoinject load")
	}
	if err := l.NonInjectorErrors(); err != nil {
		t.Errorf("NonInjectorErrors = %v, want nil: undefined: wiring.New is the injector's not-yet-generated code", err)
	}

	// Only those errors. Anything else wrong with the caller is reported,
	// and with it the compiler's echo of the whole failure.
	broken := writeLibraryInjectorModule(t, "\tundefinedHelper()\n")
	err := mustLoad(t, Config{Dir: broken}).NonInjectorErrors()
	if err == nil || !strings.Contains(err.Error(), "undefined: undefinedHelper") {
		t.Fatalf("NonInjectorErrors = %v, want the caller's own mistake reported", err)
	}

	// The same for mistakes that are no "undefined:" at all — a type
	// mismatch, and a syntax error in another of the caller's files — which
	// the pending injector cannot explain wherever they are.
	mixed := writeLibraryInjectorModule(t, "\tvar _ int = \"not an int\"\n")
	mustWriteFile(t, mixed, "cmd/app/a_broken.go", "package main\n\nfunc broken( {\n")
	err = mustLoad(t, Config{Dir: mixed}).NonInjectorErrors()
	for _, want := range []string{`"not an int"`, "a_broken.go:3:"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("NonInjectorErrors = %v, want the caller's %s reported", err, want)
		}
	}
}

// TestLoadExplainsWhereTheGoCommandCannotLoad covers the setup mistakes the
// go command already names precisely, and which servo used to replace with
// a missing servo runtime and the advice to run `servo init`.
func TestLoadExplainsWhereTheGoCommandCannotLoad(t *testing.T) {
	t.Run("no go.mod", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFile(t, dir, "x.go", "package x\n")
		_, err := Load(Config{Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "is not inside a Go module") || !strings.Contains(err.Error(), "go mod init") || strings.Contains(err.Error(), "servo init") {
			t.Fatalf("got %v, want the go command's reason and `go mod init`", err)
		}
	})
	t.Run("a go.work directory outside its modules", func(t *testing.T) {
		root, _ := writeWorkspace(t)
		_, err := Load(Config{Dir: root})
		if err == nil || !strings.Contains(err.Error(), "does not contain modules listed in go.work") || !strings.Contains(err.Error(), "point --dir at one") {
			t.Fatalf("got %v, want go's go.work message and where to run from", err)
		}
	})
	t.Run("servo not required yet", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFile(t, dir, "go.mod", "module example.com/noget\n\ngo 1.23\n")
		mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
		mustWriteFile(t, dir, "cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc wire() { servo.Build() }\n")
		_, err := Load(Config{Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "no required module provides package "+graph.ServoPackagePath) || !strings.Contains(err.Error(), "go get -tool github.com/okian/servo/v3/cmd/servo") {
			t.Fatalf("got %v, want go's reason and `go get -tool`", err)
		}
	})
	t.Run("a directory with no package", func(t *testing.T) {
		dir := writeNarrowingModule(t)
		mustWriteFile(t, dir, "docs/README.md", "docs\n")
		_, err := Load(Config{Dir: filepath.Join(dir, "docs")})
		if err == nil || !strings.Contains(err.Error(), "no Go package in") {
			t.Fatalf("got %v, want the empty selection named", err)
		}
	})
}

// TestCandidatesMatchesAScanPerInjector pins the scan-once optimization to
// the per-injector scan it replaced: the same providers and rejections, in
// the same order, for an injector in a package main and one in an
// importable package, both with unexported functions of their own.
func TestCandidatesMatchesAScanPerInjector(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/scanonce\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n\nfunc newHidden() *Server { return nil }\n\nfunc (s *Server) Name() string { return \"\" }\n")
	write("cmd/a/main.go", "package main\n\nimport \"example.com/scanonce/api\"\n\ntype Local struct{}\n\nfunc newLocal(*api.Server) *Local { return &Local{} }\n\nfunc main() {}\n")
	write("cmd/a/spec.go", "//go:build servoinject\n\npackage main\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc wire() { servo.Build(servo.Root[*Local]()) }\n")
	write("wiring/wiring.go", "package wiring\n\nimport \"example.com/scanonce/api\"\n\ntype Hub struct{}\n\nfunc newHub(*api.Server) *Hub { return &Hub{} }\n\nfunc Exported() *Hub { return nil }\n")
	write("wiring/spec.go", "//go:build servoinject\n\npackage wiring\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc wire() { servo.Build(servo.Root[*Hub]()) }\n")
	runGoModTidy(t, dir)

	l := mustLoad(t, Config{Dir: dir})
	specs, err := FindSpecs(l)
	if err != nil || len(specs) != 2 {
		t.Fatalf("FindSpecs = %d specs, %v; want 2", len(specs), err)
	}
	for _, s := range specs {
		gotA, gotR := l.Candidates(s.InjectorPkg)
		wantA, wantR := graph.ScanCandidates(l.All, s.InjectorPkg.PkgPath)
		if got, want := describeProviders(gotA), describeProviders(wantA); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: candidates differ from a scan of its own:\ngot  %v\nwant %v", s.InjectorPkg.PkgPath, got, want)
		}
		if got, want := describeRejected(gotR), describeRejected(wantR); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rejections differ from a scan of its own:\ngot  %v\nwant %v", s.InjectorPkg.PkgPath, got, want)
		}
	}
	// Both injectors' own unexported constructors are in, and only for
	// their own injector.
	for _, c := range []struct{ injector, name string }{
		{"example.com/scanonce/cmd/a", "main.newLocal"},
		{"example.com/scanonce/wiring", "wiring.newHub"},
	} {
		accepted, _ := l.Candidates(l.ByPath[c.injector])
		if !strings.Contains(strings.Join(describeProviders(accepted), "\n"), c.name+" ") {
			t.Errorf("%s's candidates lack its own %s", c.injector, c.name)
		}
	}
}

func describeProviders(ps []*graph.Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = fmt.Sprintf("%s %s %s %v %v %v %v %v", p.Name, p.Pkg, p.Pos, p.Result, p.Params, p.HasCleanup, p.HasError, p.Unexported)
	}
	return out
}

func describeRejected(rs []graph.Rejected) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		result := ""
		if r.Result != nil {
			result = r.Result.String()
		}
		out[i] = fmt.Sprintf("%s %s %s %s %s %s", r.Name, r.Pkg, r.Pos, r.Reason, r.Recv, result)
	}
	return out
}

// TestLoadIsDeterministic covers the walk that built All in map order: the
// package an export-data failure was reported against, among others,
// changed from run to run.
func TestLoadIsDeterministic(t *testing.T) {
	dir := writeFixtureModule(t, "")
	first := pkgPaths(mustLoad(t, Config{Dir: dir}).All)
	for i := 0; i < 2; i++ {
		if again := pkgPaths(mustLoad(t, Config{Dir: dir}).All); !reflect.DeepEqual(first, again) {
			t.Fatalf("All differs between two loads of the same module:\n%v\n%v", first, again)
		}
	}
}
