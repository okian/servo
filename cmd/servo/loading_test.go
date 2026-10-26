package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/load"
)

// writeRoutesModule is a module whose injector, cmd/app, imports neither
// of store.Store's two implementations nor the package declaring its one
// route — the layout where a load narrowed to --dir's imports served no
// routes and auto-bound whichever implementation main.go happened to
// import.
func writeRoutesModule(t *testing.T, bind bool) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/dirroutes\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	write("memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc (m *Mem) Get(key string) string { return \"\" }\n\nfunc New() *Mem { return &Mem{} }\n")
	write("redis/redis.go", "package redis\n\ntype Redis struct{}\n\nfunc (r *Redis) Get(key string) string { return \"\" }\n\nfunc New() *Redis { return &Redis{} }\n")
	write("api/api.go", "package api\n\nimport (\n\t\"example.com/dirroutes/store\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\ntype Server struct{ s store.Store }\n\nfunc New(s store.Store) *Server { return &Server{s: s} }\n\nfunc NewHTTPConfig() *servo.HTTPConfig { return &servo.HTTPConfig{} }\n")
	write("handlers/handlers.go", "package handlers\n\nimport (\n\t\"context\"\n\n\t\"github.com/okian/servo/v3/servo\"\n)\n\ntype Pong struct{ OK bool }\n\n//servo:get /ping\nfunc Ping(ctx context.Context) (servo.Json[Pong], error) { return servo.JSON(Pong{OK: true}), nil }\n")
	write("cmd/app/main.go", "package main\n\nimport _ \"example.com/dirroutes/memory\"\n\nfunc main() {}\n")
	binds, imports := "", ""
	if bind {
		binds = "\t\tservo.Bind[store.Store, *memory.Mem](),\n"
		imports = "\t\"example.com/dirroutes/memory\"\n\t\"example.com/dirroutes/store\"\n"
	}
	write("cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\"example.com/dirroutes/api\"\n"+imports+"\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(\n\t\tservo.Root[*api.Server](),\n"+binds+"\t\tservo.HTTP(),\n\t)\n}\n")
	runGoModTidy(t, dir)
	return dir
}

// TestGenerateFromAnInjectorsDirectoryIsGenerateFromTheRoot covers the
// review's two reproductions of --dir narrowing the graph, through the
// command `go generate` actually runs: from the injector's own directory,
// an interface the module root calls ambiguous was silently auto-bound,
// and an HTTP server was emitted serving none of the module's routes.
func TestGenerateFromAnInjectorsDirectoryIsGenerateFromTheRoot(t *testing.T) {
	ambiguous := writeRoutesModule(t, false)
	err := runGenerate(cfg(filepath.Join(ambiguous, "cmd", "app")))
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider for example.com/dirroutes/store.Store") {
		t.Fatalf("generate --dir cmd/app = %v, want the ambiguity the module root reports", err)
	}

	dir := writeRoutesModule(t, true)
	if err := runGenerate(cfg(filepath.Join(dir, "cmd", "app"))); err != nil {
		t.Fatalf("generate --dir cmd/app: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "cmd", "app", generatedFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"/ping"`) {
		t.Errorf("the server generated from cmd/app does not serve /ping, declared in a package nothing imports yet:\n%s", out)
	}
	// And the root agrees with the file written from the injector's
	// directory, which is the property CI's `servo check` relies on.
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check from the module root on the file generated from cmd/app: %v", err)
	}
}

// TestScopeFlagReachesTheLoad covers --scope from the command line to
// load.Config, for every command that loads packages: registered once in
// registerLoadFlags, defaulting to module, and refused by the loader when
// it is neither value.
func TestScopeFlagReachesTheLoad(t *testing.T) {
	fs := newFlagSet("check")
	read := registerLoadFlags(fs, "`path`")
	if _, err := parseFlags(fs, nil); err != nil {
		t.Fatal(err)
	}
	if got := read().Scope; got != load.ScopeModule {
		t.Errorf("default Scope = %q, want %q", got, load.ScopeModule)
	}

	fs = newFlagSet("check")
	read = registerLoadFlags(fs, "`path`")
	if _, err := parseFlags(fs, []string{"--scope", "workspace"}); err != nil {
		t.Fatal(err)
	}
	if got := read().Scope; got != load.ScopeWorkspace {
		t.Errorf("--scope workspace gave Scope = %q", got)
	}
	if inv := invocationOf(fs); !strings.Contains(inv.command("generate", nil), "--scope=workspace") {
		t.Errorf("a suggested command drops --scope: %s", inv.command("generate", nil))
	}

	for _, name := range loadingCommands() {
		if lookupCommand(name).flagSet().Lookup("scope") == nil {
			t.Errorf("servo %s does not take --scope", name)
		}
	}

	if err := run([]string{"check", "--dir", t.TempDir(), "--scope", "everything"}); err == nil || !strings.Contains(err.Error(), "--scope=everything") {
		t.Errorf("check --scope everything = %v, want the scope refused", err)
	}
}

// TestLoadModuleWarnsAboutAnOlderRuntime covers the printing half of the
// capability skew warning: a runtime declaring fewer capabilities than this
// CLI detects is not an error, and every loading command says so once, on
// stderr.
func TestLoadModuleWarnsAboutAnOlderRuntime(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("runtime/go.mod", "module github.com/okian/servo/v3\n\ngo 1.23\n")
	write("runtime/servo/servo.go", "package servo\n\nimport \"context\"\n\ntype Marker struct{}\n\nfunc Build(...Marker) {}\n\nfunc Root[T any]() Marker { return Marker{} }\n\ntype Initializer interface{ Init(ctx context.Context) error }\n")
	write("app/go.mod", "module example.com/oldruntime\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => ../runtime\n")
	write("app/spec/spec.go", "//go:build servoinject\n\npackage spec\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc Wire() { servo.Build() }\n")

	var err error
	stderr := captureStderr(t, func() { _, _, _, _, err = loadModule(cfg(filepath.Join(dir, "app"))) })
	if err != nil {
		t.Fatalf("loadModule: %v", err)
	}
	if !strings.Contains(stderr, "servo: warning: github.com/okian/servo/v3/servo declares none of Runner") || !strings.Contains(stderr, "go tool servo") {
		t.Errorf("stderr = %q, want the capability skew warning", stderr)
	}
}

// TestScopeDecidesWhatAWorkspaceSiblingIs covers the review's go.work
// reproductions end to end. Every module go.work lists is Main to the go
// command, and servo took that at its word: generate wrote the sibling's
// config loader into the sibling's directory, where a GOWORK=off checkout —
// CI's — never regenerates it. Under the default scope the sibling is a
// dependency, as GOWORK=off makes it; --scope=workspace keeps the old
// reading on request.
func TestScopeDecidesWhatAWorkspaceSiblingIs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, root, rel, content) }
	write("go.work", "go "+strings.TrimPrefix(runtime.Version(), "go")+"\n\nuse (\n\t./app\n\t./lib\n)\n")
	write("lib/go.mod", "module example.com/lib\n\ngo 1.23\n")
	write("lib/settings/settings.go", "package settings\n\n//servo:config prefix=LIB\ntype Settings struct {\n\tRegion string `config:\"region,default=eu\"`\n}\n")
	write("app/go.mod", "module example.com/app\n\ngo 1.23\n\nrequire (\n\texample.com/lib v0.0.0\n\tgithub.com/okian/servo/v3 v3.0.0\n)\n\nreplace (\n\texample.com/lib => ../lib\n\tgithub.com/okian/servo/v3 => "+repoRoot(t)+"\n)\n")
	write("app/api/api.go", "package api\n\nimport \"example.com/lib/settings\"\n\ntype Server struct{}\n\nfunc New(cfg settings.Settings) *Server { return &Server{} }\n")
	write("app/cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\"example.com/app/api\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() { servo.Build(servo.Root[*api.Server]()) }\n")
	app := filepath.Join(root, "app")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = app
	tidy.Env = append(tidy.Environ(), "GOWORK=off")
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}
	companion := filepath.Join(root, "lib", "settings", "servo_config_gen.go")

	err := runGenerate(cfg(app))
	if err == nil || !strings.Contains(err.Error(), "no provider for example.com/lib/settings.Settings") {
		t.Fatalf("generate under --scope=module = %v, want lib's config type treated as a dependency's", err)
	}
	if _, err := os.Stat(companion); err == nil {
		t.Fatal("generate under --scope=module wrote into the sibling module")
	}

	if err := runGenerate(load.Config{Dir: app, Scope: load.ScopeWorkspace}); err != nil {
		t.Fatalf("generate --scope=workspace: %v", err)
	}
	if _, err := os.Stat(companion); err != nil {
		t.Errorf("generate --scope=workspace did not treat the sibling as part of the main module: %v", err)
	}
}
