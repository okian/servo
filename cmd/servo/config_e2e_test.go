package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigModule materializes the //servo:config shape end to end: an
// unexported config struct whose loader is generated into its own package
// (store), a constructor in that package consuming it, and an injector
// that optionally declares a JSON config file — JSON so the fixture stays
// stdlib-only and hermetic (yaml/toml decoders are covered by
// internal/emit's unit tests; nothing about them differs but the
// Unmarshal call).
func writeConfigModule(t *testing.T, modulePath string, withConfigFile bool) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)

	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module `+modulePath+`

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)

	write("store/store.go", `package store

import (
	"fmt"
	"time"
)

//servo:config prefix=APP
type config struct {
	dsn      string        `+"`config:\"dsn,required\"`"+`
	maxConns int32         `+"`config:\"max_conns,default=10\"`"+`
	timeout  time.Duration `+"`config:\"timeout,default=30s\"`"+`
	token    string        `+"`config:\"token,secret\"`"+`
}

type Store struct{ cfg config }

func New(cfg config) (*Store, error) { return &Store{cfg: cfg}, nil }

func (s *Store) Describe() string {
	return fmt.Sprintf("dsn=%s max_conns=%d timeout=%s token_len=%d",
		s.cfg.dsn, s.cfg.maxConns, s.cfg.timeout, len(s.cfg.token))
}
`)

	configFileLine := ""
	if withConfigFile {
		configFileLine = "\t\tservo.ConfigFile(\"config.json\"),\n"
	}
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"`+modulePath+`/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*store.Store](),
`+configFileLine+`	)
}
`)

	write("cmd/app/main.go", `package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	a, err := New(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(a.store.Describe())
}
`)

	runGoModTidy(t, dir)
	return dir
}

// runConfigApp builds and runs the fixture binary with exactly the given
// APP_*/CONFIG_FILE environment, from the module root so a relative
// config.json resolves.
func runConfigApp(t *testing.T, dir string, env ...string) (stdout, stderr string, ok bool) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "app")
	build := exec.Command("go", "build", "-o", bin, "./cmd/app")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build fixture: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err == nil
}

func TestConfigEnvOnlyEndToEnd(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfgenv", false)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	companion, err := os.ReadFile(filepath.Join(dir, "store", "servo_config_gen.go"))
	if err != nil {
		t.Fatalf("companion loader not written: %v", err)
	}
	if !strings.Contains(string(companion), "func ServoConfig() (config, error)") {
		t.Fatalf("companion is not the env-only loader:\n%s", companion)
	}
	injector, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(injector), "config, err := store.ServoConfig()") {
		t.Fatalf("injector does not call the generated loader:\n%s", injector)
	}

	// The compiler is the real reviewer of generated code.
	runGoBuild(t, dir, "")

	stdout, _, ok := runConfigApp(t, dir,
		"APP_DSN=postgres://db", "APP_MAX_CONNS=40", "APP_TIMEOUT=90s", "APP_TOKEN=hunter2")
	if !ok {
		t.Fatalf("app failed; stdout=%q", stdout)
	}
	if want := "dsn=postgres://db max_conns=40 timeout=1m30s token_len=7\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}

	// Defaults apply when only the required variable is set.
	stdout, _, ok = runConfigApp(t, dir, "APP_DSN=x")
	if !ok || stdout != "dsn=x max_conns=10 timeout=30s token_len=0\n" {
		t.Fatalf("defaults run: ok=%v stdout=%q", ok, stdout)
	}

	// A missing required variable is a startup error naming it.
	_, stderr, ok := runConfigApp(t, dir)
	if ok || !strings.Contains(stderr, "missing required configuration: APP_DSN") {
		t.Fatalf("missing-required run: ok=%v stderr=%q", ok, stderr)
	}

	// A malformed value names the variable and echoes the value — this
	// field is not secret.
	_, stderr, ok = runConfigApp(t, dir, "APP_DSN=x", "APP_MAX_CONNS=ten")
	if ok || !strings.Contains(stderr, `APP_MAX_CONNS: not a valid int32: "ten"`) {
		t.Fatalf("malformed run: ok=%v stderr=%q", ok, stderr)
	}
}

func TestConfigJSONFileEndToEnd(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfgjson", true)
	mustWriteFile(t, dir, "config.json", `{"app": {"dsn": "from-file", "max_conns": 25}}`)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	companion, err := os.ReadFile(filepath.Join(dir, "store", "servo_config_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(companion), "func ServoConfig(file map[string]any) (config, error)") {
		t.Fatalf("companion is not the file-reading loader:\n%s", companion)
	}
	runGoBuild(t, dir, "")

	// File values fill what the environment doesn't; defaults cover the rest.
	stdout, _, ok := runConfigApp(t, dir)
	if !ok || stdout != "dsn=from-file max_conns=25 timeout=30s token_len=0\n" {
		t.Fatalf("file run: ok=%v stdout=%q", ok, stdout)
	}

	// The environment always wins over the file.
	stdout, _, ok = runConfigApp(t, dir, "APP_MAX_CONNS=50")
	if !ok || stdout != "dsn=from-file max_conns=50 timeout=30s token_len=0\n" {
		t.Fatalf("env-wins run: ok=%v stdout=%q", ok, stdout)
	}

	// CONFIG_FILE points somewhere explicit, so that file must exist.
	_, stderr, ok := runConfigApp(t, dir, "CONFIG_FILE=missing.json", "APP_DSN=x")
	if ok || !strings.Contains(stderr, "missing.json") {
		t.Fatalf("explicit-missing run: ok=%v stderr=%q", ok, stderr)
	}

	// A different extension family was not compiled in.
	_, stderr, ok = runConfigApp(t, dir, "CONFIG_FILE=config.yaml", "APP_DSN=x")
	if ok || !strings.Contains(stderr, "unsupported extension") {
		t.Fatalf("wrong-extension run: ok=%v stderr=%q", ok, stderr)
	}

	// The *declared* path missing is fine — env still serves everything.
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, ok = runConfigApp(t, dir, "APP_DSN=env-only")
	if !ok || stdout != "dsn=env-only max_conns=10 timeout=30s token_len=0\n" {
		t.Fatalf("absent-default-file run: ok=%v stdout=%q stderr=%q", ok, stdout, stderr)
	}
}

func TestCheckCoversCompanionLoaders(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfgcheck", false)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Fatalf("fresh module reported stale: %v", err)
	}

	companionPath := filepath.Join(dir, "store", "servo_config_gen.go")
	stale, err := os.ReadFile(companionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companionPath, append(stale, []byte("\n// drifted\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	err = runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "servo_config_gen.go is stale") {
		t.Fatalf("got err=%v, want the companion staleness report", err)
	}

	if err := os.Remove(companionPath); err != nil {
		t.Fatal(err)
	}
	err = runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "servo_config_gen.go does not exist") {
		t.Fatalf("got err=%v, want the companion missing report", err)
	}
}

func TestConfigCommand(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfgcmd", true)
	var runErr error
	out := captureStdout(t, func() { runErr = runConfig(cfg(dir), false) })
	if runErr != nil {
		t.Fatalf("runConfig: %v", runErr)
	}
	for _, want := range []string{
		"configuration for example.com/cfgcmd/cmd/app",
		"file: config.json (override the path with CONFIG_FILE",
		"APP_DSN", "app.dsn", "required",
		"APP_MAX_CONNS", "int32", "default 10",
		"APP_TIMEOUT", "time.Duration", "default 30s",
		"APP_TOKEN", "string (secret)", "zero value",
		"config.dsn", // field column, so the reader can jump to the declaration
	} {
		if !strings.Contains(out, want) {
			t.Errorf("servo config output missing %q\n---\n%s", want, out)
		}
	}
}

// TestConfigUsedOnlyByOverride pins the corner where a config enters the
// graph solely through a servo.Override: NewTestApp calls its loader, so
// the companion must be written even though the production graph never
// touches the type.
func TestConfigUsedOnlyByOverride(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/cfgoverride

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	write("store/store.go", `package store

type Store interface{ Get(key string) string }
`)
	write("memory/memory.go", `package memory

type Mem struct{}

func (m *Mem) Get(key string) string { return "" }

func New() *Mem { return &Mem{} }
`)
	write("pg/pg.go", `package pg

//servo:config prefix=PG
type config struct {
	dsn string `+"`config:\"dsn,required\"`"+`
}

type PG struct{ dsn string }

func (p *PG) Get(key string) string { return p.dsn }

func New(cfg config) *PG { return &PG{dsn: cfg.dsn} }
`)
	write("api/api.go", `package api

import "example.com/cfgoverride/store"

type Server struct{ s store.Store }

func New(s store.Store) *Server { return &Server{s: s} }
`)
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/cfgoverride/api"
	"example.com/cfgoverride/memory"
	"example.com/cfgoverride/pg"
	"example.com/cfgoverride/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
		servo.Override[store.Store, *pg.PG](),
	)
}
`)
	write("cmd/app/main.go", `package main

import "context"

func main() { _, _ = New(context.Background()) }
`)
	runGoModTidy(t, dir)

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pg", "servo_config_gen.go")); err != nil {
		t.Fatalf("companion for the override-only config not written: %v", err)
	}
	// go vet type-checks _test.go files too, so this is the check that
	// NewTestApp's call into pg.ServoConfig actually resolves.
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet (which compiles servo_gen_test.go): %v\n%s", err, out)
	}
}

// TestConfigNodeInInspectionCommands runs every graph-reading command over
// a config node. Config nodes have no Provider, so each of these paths
// carries a Kind branch — this is the test that a missed one panics.
func TestConfigNodeInInspectionCommands(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfginspect", false)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	var err error
	out := captureStdout(t, func() { err = runExplain(cfg(dir), "store.config", false) })
	if err != nil {
		t.Fatalf("runExplain: %v", err)
	}
	for _, want := range []string{"store.ServoConfig (generated, env prefix APP)", "config directive", "loaded by New before construction"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q\n---\n%s", want, out)
		}
	}

	out = captureStdout(t, func() { err = runWhy(cfg(dir), "store.config", false) })
	if err != nil {
		t.Fatalf("runWhy: %v", err)
	}
	if !strings.Contains(out, "-> example.com/cfginspect/store.config") {
		t.Errorf("why output missing the config edge\n---\n%s", out)
	}

	for _, format := range []string{"text", "json", "dot", "mermaid"} {
		out = captureStdout(t, func() { err = runGraph(cfg(dir), format) })
		if err != nil {
			t.Fatalf("runGraph(%s): %v", format, err)
		}
		if !strings.Contains(out, "store.config") {
			t.Errorf("graph %s output missing the config node\n---\n%s", format, out)
		}
	}

	out = captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err != nil {
		t.Fatalf("runDoctor: %v\n%s", err, out)
	}
}

// TestConfigAgreementMismatch pins the module-level contract: one
// companion loader cannot have two signatures, so every injector using a
// config must agree about ConfigFile.
func TestConfigAgreementMismatch(t *testing.T) {
	dir := writeConfigModule(t, "example.com/cfgmix", true)
	// A second injector using the same config, without a ConfigFile.
	mustWriteFile(t, dir, "cmd/worker/spec.go", `//go:build servoinject

package main

import (
	"example.com/cfgmix/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*store.Store](),
	)
}
`)
	mustWriteFile(t, dir, "cmd/worker/main.go", `package main

import "context"

func main() { _, _ = New(context.Background()) }
`)

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "declare a ConfigFile in both injectors or neither") {
		t.Fatalf("got err=%v, want the agreement refusal", err)
	}
	// Nothing may have been written: the refusal must come before any
	// injector's files land, or the tree is half-regenerated.
	if _, statErr := os.Stat(filepath.Join(dir, "cmd", "app", "servo_gen.go")); !os.IsNotExist(statErr) {
		t.Errorf("servo_gen.go exists after a refused generation (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "store", "servo_config_gen.go")); !os.IsNotExist(statErr) {
		t.Errorf("servo_config_gen.go exists after a refused generation (stat err = %v)", statErr)
	}
}
