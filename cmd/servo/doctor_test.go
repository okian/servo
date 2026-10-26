package main

import (
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/load"
	"golang.org/x/tools/go/packages"
)

// TestRunDoctorAggregatesEveryInjector covers a multi-injector module
// where one injector is fully healthy (generated, fresh, git-tracked) and
// another is missing its generated file entirely: doctor previously used
// FindSpec (singular), which errors outright ("multiple injectors found
// — pass --dir") the moment more than one exists, making it unusable for
// a multi-injector module at all. It must now report on every injector
// found, the same scope generate/check already use, and still fail
// overall because of the one real problem.
func TestRunDoctorAggregatesEveryInjector(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/doctormulti\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "worker/worker.go", "package worker\n\ntype Consumer struct{}\n\nfunc New() *Consumer { return &Consumer{} }\n")
	mustWriteFile(t, dir, "cmd/apisvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/doctormulti/api"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*api.Server]()) }
`)
	mustWriteFile(t, dir, "cmd/workersvc/spec.go", `//go:build servoinject

package main

import (
	"example.com/doctormulti/worker"
	"github.com/okian/servo/v3/servo"
)

func wire() { servo.Build(servo.Root[*worker.Consumer]()) }
`)
	runGoModTidy(t, dir)

	// Generate only apisvc, leaving workersvc's servo_gen.go missing.
	if err := runGenerate(cfg(filepath.Join(dir, "cmd", "apisvc"))); err != nil {
		t.Fatalf("runGenerate(cfg(apisvc)): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "apisvc", generatedFileName)); err != nil {
		t.Fatalf("expected apisvc's servo_gen.go to exist: %v", err)
	}

	var doctorErr error
	out := captureStdout(t, func() { doctorErr = runDoctor(cfg(dir)) })
	if doctorErr == nil {
		t.Fatal("expected an overall failure: workersvc has no generated file")
	}
	if !strings.Contains(out, "apisvc") || !strings.Contains(out, "workersvc") {
		t.Errorf("expected both injectors named in the report, got:\n%s", out)
	}
	if !strings.Contains(out, "generated file present") {
		t.Errorf("expected apisvc reported healthy, got:\n%s", out)
	}
	if !strings.Contains(out, "generated file missing") {
		t.Errorf("expected workersvc reported missing its generated file, got:\n%s", out)
	}
}

// TestRunDoctorFailsOnNonInjectorBuildErrors covers doctor's own
// NonInjectorErrors check: a real compile error in a sibling package,
// unrelated to the injector itself, must be reported — generate/check
// already gate on this before ever writing a file, so doctor reporting
// "all green" here would be a false all-clear for something generate
// would immediately fail on.
func TestRunDoctorFailsOnNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorbrokensibling", true, "")
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected runDoctor to fail on a build error outside the injector")
	}
	if !strings.Contains(out, "[FAIL]") || !strings.Contains(out, "build errors outside the injector") {
		t.Errorf("expected a build-errors FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenModuleDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/nolib\n\ngo 1.23\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil || !strings.Contains(err.Error(), "problems found") {
		t.Fatalf("got err=%v, want 'problems found'", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "load module") {
		t.Errorf("expected a 'load module' FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenSpecMissing(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/nospec\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nimport _ \"github.com/okian/servo/v3/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil || !strings.Contains(err.Error(), "problems found") {
		t.Fatalf("got err=%v, want 'problems found'", err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "find spec file") {
		t.Errorf("expected a 'find spec file' FAIL line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenGeneratedFileMissing(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctormissing", true, "")

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected an error when the generated file is missing")
	}
	if !strings.Contains(out, "generated file missing") {
		t.Errorf("expected a 'generated file missing' line, got:\n%s", out)
	}
}

func TestRunDoctorFailsWhenGeneratedFileStale(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorstale", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	genPath := filepath.Join(dir, "cmd", "app", generatedFileName)
	if err := os.WriteFile(genPath, []byte("//go:build !servoinject\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err == nil {
		t.Fatal("expected an error when the generated file is stale")
	}
	if !strings.Contains(out, "generated file is stale") {
		t.Errorf("expected a 'generated file is stale' line, got:\n%s", out)
	}
}

func TestRunDoctorWarnsWhenNotTrackedByGit(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctoruntracked", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err != nil {
		t.Fatalf("runDoctor should succeed (untracked is a warning, not a failure): %v", err)
	}
	if !strings.Contains(out, "[WARN]") || !strings.Contains(out, "may not be committed") {
		t.Errorf("expected an untracked-by-git WARN line, got:\n%s", out)
	}
}

func TestRunDoctorCleanWhenTrackedByGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := writeAppModule(t, "example.com/doctorclean", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	// Every invocation is isolated from the developer's own git
	// configuration. core.hooksPath is frequently set globally, and those
	// hooks would otherwise run inside this throwaway repository and fail
	// the commit for reasons that have nothing to do with servo (a
	// protected-branch guard, a formatter, a spell checker). Signing is
	// disabled for the same reason: a global commit.gpgsign would block a
	// commit no interactive agent is present to authorize. Both make the
	// test pass in CI and fail on a contributor's machine.
	isolated := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgsign=false",
	}
	for _, args := range [][]string{
		{"init"},
		{"add", "."},
		{"-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append(append([]string{}, isolated...), args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	if err != nil {
		t.Fatalf("runDoctor on a fully clean, committed tree: %v", err)
	}
	if !strings.Contains(out, "generated file is tracked by git") {
		t.Errorf("expected a tracked-by-git OK line, got:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("expected no FAIL lines, got:\n%s", out)
	}
}

func TestTrackedByGitOnNonRepo(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "servo_gen.go", "package main\n")
	if trackedByGit(dir, filepath.Join(dir, "servo_gen.go")) {
		t.Error("trackedByGit on a directory with no .git should be false")
	}
}

// TestTrackedByGitFallsBackWhenPathsCannotBeMadeRelative covers the
// filepath.Rel error branch: a relative dir paired with an absolute path
// (dir="." is the CLI's own default, and go/packages always reports
// absolute file positions) can't be resolved to a relative path by lexical
// analysis alone, so trackedByGit must fall back to using path as-is
// rather than propagating the error or panicking.
func TestTrackedByGitFallsBackWhenPathsCannotBeMadeRelative(t *testing.T) {
	if trackedByGit(".", "/absolute/path/servo_gen.go") {
		t.Error("trackedByGit with an unrelatable dir/path pair should be false, not panic or true")
	}
}

// TestReportVariantsSurfacesAnUnreadableInventory: every other failure in
// doctor is reported and the run continues, so it would be tempting to let
// this one return nothing too. It must not. The variant inventory is the
// only thing in servo that ever notices an orphaned or unverified
// generated file, and "no other variants" is precisely what it prints for
// a healthy single-variant project — so an inventory that could not be
// read has to be distinguishable from an empty one, and nothing may be
// reported off the back of it.
func TestReportVariantsSurfacesAnUnreadableInventory(t *testing.T) {
	// An unmatched '[' in the injector's own directory name is legal on
	// every filesystem servo runs on, and makes the inventory's glob
	// pattern invalid.
	dir := filepath.Join(t.TempDir(), "proj[1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := &load.Spec{
		Pos:         token.Position{Filename: filepath.Join(dir, "spec.go")},
		InjectorPkg: &packages.Package{},
	}

	var reported []string
	err := reportVariants(spec, generatedFileName, invocation{}, func(_ bool, format string, args ...any) {
		reported = append(reported, fmt.Sprintf(format, args...))
	})
	if err == nil {
		t.Fatal("reportVariants = nil, want the unreadable inventory reported")
	}
	if len(reported) != 0 {
		t.Errorf("reportVariants drew conclusions from an inventory it could not read: %v", reported)
	}
}

// doctorOutput runs doctor over dir and returns what it printed and
// whether it failed.
func doctorOutput(t *testing.T, dir string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runDoctor(cfg(dir)) })
	return out, err
}

// TestDoctorFailsWhenGoGenerateCannotSeeTheDirective is the module the
// README warns about: the directive sits in the servoinject-gated spec
// file, go generate honours build constraints, and `go generate ./...`
// exits 0 having run nothing. Doctor used to call that module healthy.
func TestDoctorFailsWhenGoGenerateCannotSeeTheDirective(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctordeaddirective", true, "")
	mustWriteFile(t, dir, "cmd/app/gen.go", "//go:build servoinject\n\npackage main\n\n//go:generate go tool servo generate\n")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	out, err := doctorOutput(t, dir)
	if err == nil {
		t.Fatalf("doctor passed a module whose only directive go generate never runs:\n%s", out)
	}
	for _, want := range []string{"[FAIL] go generate never runs `servo generate` for this injector", "gen.go:5", "`//go:build servoinject`", "servo_generate.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// TestDoctorChecksTheToolDirectiveGoToolNeeds: `go tool servo` resolves
// only with go.mod's tool directive, which `go get -tool` writes — the
// step anyone skipping servo init's printed advice misses, and go
// generate then fails with `no such tool "servo"`.
func TestDoctorChecksTheToolDirectiveGoToolNeeds(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctortool", true, "")
	mustWriteFile(t, dir, "cmd/app/servo_generate.go", "package main\n\n//go:generate go tool servo generate\n")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	out, err := doctorOutput(t, dir)
	if err == nil || !strings.Contains(out, "has no tool directive for it") || !strings.Contains(out, "go get -tool "+servoToolPath) {
		t.Fatalf("doctor err=%v, want the missing tool directive named with its fix:\n%s", err, out)
	}
	if !strings.Contains(out, "[OK  ] go generate runs `servo generate` for this injector: "+filepath.Join(dir, "cmd", "app", "servo_generate.go")+":3") {
		t.Errorf("an untagged directive over the injector should be reported as reaching it:\n%s", out)
	}

	gomod, readErr := os.ReadFile(filepath.Join(dir, "go.mod"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	mustWriteFile(t, dir, "go.mod", string(gomod)+"\ntool "+servoToolPath+"\n")
	runGoModTidy(t, dir)
	out, _ = doctorOutput(t, dir)
	if !strings.Contains(out, "declares the servo tool that `go tool servo` runs") || strings.Contains(out, "has no tool directive") {
		t.Errorf("with the tool directive in place doctor should say so:\n%s", out)
	}
}

// TestDoctorOnAModuleThatDoesNotUseGoGenerate: regenerating from make or
// CI is a legitimate setup, so a module with no servo directive at all is
// told, not failed — a doctor that can never go green gets switched off.
func TestDoctorOnAModuleThatDoesNotUseGoGenerate(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctornodirective", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	out, err := doctorOutput(t, dir)
	if err != nil {
		t.Fatalf("doctor failed a module that regenerates without go generate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[WARN] no //go:generate line in this module runs servo") {
		t.Errorf("doctor should still say go generate will not regenerate it:\n%s", out)
	}
}

// TestDoctorFailsAnInjectorTheModulesDirectivesMiss: once a module uses
// go generate for servo, an injector none of its lines covers — or only
// for another variant — is left stale by `go generate ./...`.
func TestDoctorFailsAnInjectorTheModulesDirectivesMiss(t *testing.T) {
	dir := writeTwoInjectorVariantModule(t, "example.com/doctorpartial")
	// cmd/app's line generates both of its variants; nothing covers
	// cmd/worker, and a prod-only line would not cover its default.
	mustWriteFile(t, dir, "cmd/app/servo_generate.go", "package main\n\n//go:generate servo generate\n//go:generate servo generate --tags=prod\n")
	mustWriteFile(t, dir, "cmd/worker/servo_generate.go", "package main\n\n//go:generate servo generate --tags=prod\n")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	out, err := doctorOutput(t, dir)
	if err == nil {
		t.Fatalf("doctor passed an injector go generate never regenerates:\n%s", out)
	}
	app := strings.Index(out, "cmd/app --")
	worker := strings.Index(out, "cmd/worker --")
	if app < 0 || worker < 0 {
		t.Fatalf("expected both injectors' sections:\n%s", out)
	}
	appSection, workerSection := out[app:worker], out[worker:]
	if app > worker {
		appSection, workerSection = out[app:], out[worker:app]
	}
	if !strings.Contains(appSection, "[OK  ] go generate runs `servo generate`") {
		t.Errorf("cmd/app is covered by its own line:\n%s", appSection)
	}
	if !strings.Contains(workerSection, "[FAIL] no //go:generate line runs `servo generate` over this injector") {
		t.Errorf("cmd/worker is covered only for prod, so the default variant should fail:\n%s", workerSection)
	}
}

// TestDoctorRunsTheConfigAgreementCheck: generate and check refuse
// injectors that disagree about a shared config's loader signature, and
// doctor used to pass the same module.
func TestDoctorRunsTheConfigAgreementCheck(t *testing.T) {
	dir := writeConfigModule(t, "example.com/doctorcfgmix", true)
	mustWriteFile(t, dir, "cmd/worker/spec.go", `//go:build servoinject

package main

import (
	"example.com/doctorcfgmix/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*store.Store](),
	)
}
`)
	mustWriteFile(t, dir, "cmd/worker/main.go", "package main\n\nimport \"context\"\n\nfunc main() { _, _ = New(context.Background()) }\n")

	out, err := doctorOutput(t, dir)
	if err == nil || !strings.Contains(out, "[FAIL] config loaders:") || !strings.Contains(out, "declare a ConfigFile in both injectors or neither") {
		t.Fatalf("doctor err=%v, want the config agreement failure generate would report:\n%s", err, out)
	}
}

func TestServoInvocation(t *testing.T) {
	cases := []struct {
		line   string
		ok     bool
		goTool bool
		dir    string
		tags   []string
	}{
		{"go tool servo generate", true, true, ".", nil},
		{"go tool servo", true, true, ".", nil},
		{"go tool " + servoToolPath + " generate --dir=../..", true, true, "../..", nil},
		{"go run github.com/okian/servo/v3/cmd/servo@v3.4.1 generate --tags prod", true, false, ".", []string{"prod"}},
		{"go run -mod=mod ../../cmd/servo --dir ./cmd/app", true, false, "./cmd/app", nil},
		{"go run -tags x ./cmd/servo generate", true, false, ".", nil},
		{"servo generate --tags=prod,integration", true, false, ".", []string{"integration", "prod"}},
		{"/usr/local/bin/servo", true, false, ".", nil},
		{"go tool servo check", false, false, "", nil},
		{"go tool servo-vet ./...", false, false, "", nil},
		{"go run go.uber.org/mock/mockgen -source=x.go", false, false, "", nil},
		{"stringer -type=Pill", false, false, "", nil},
	}
	for _, c := range cases {
		words := splitGenerateLine(c.line, "gen.go", 1)
		args, goTool, ok := servoInvocation(words)
		var dir string
		var tags []string
		if ok {
			var isGenerate bool
			var err error
			dir, tags, isGenerate, err = generateRunOf(args)
			if err != nil {
				t.Errorf("%q: %v", c.line, err)
			}
			ok = isGenerate
		}
		if ok != c.ok || (ok && (goTool != c.goTool || dir != c.dir || !slices.Equal(tags, c.tags))) {
			t.Errorf("%q: ok=%v goTool=%v dir=%q tags=%v, want ok=%v goTool=%v dir=%q tags=%v", c.line, ok, goTool, dir, tags, c.ok, c.goTool, c.dir, c.tags)
		}
	}
	// A flag generate does not take makes the line fail when go generate
	// runs it, which doctor reports rather than counting as coverage.
	if _, _, _, err := generateRunOf([]string{"generate", "--no-such-flag"}); err == nil {
		t.Error("generateRunOf accepted a flag generate does not take")
	}
}

func TestSplitGenerateLineFollowsGoGenerate(t *testing.T) {
	t.Setenv("SERVO_DOCTOR_TEST_DIR", "cmd/app")
	got := splitGenerateLine(`servo  generate "--dir" $SERVO_DOCTOR_TEST_DIR $GOFILE`, "/m/x/gen.go", 3)
	want := []string{"servo", "generate", "--dir", "cmd/app", "gen.go"}
	if !slices.Equal(got, want) {
		t.Errorf("splitGenerateLine = %q, want %q", got, want)
	}
}

func TestGenerateDirectiveCovers(t *testing.T) {
	d := generateDirective{root: filepath.FromSlash("/m/cmd")}
	for dir, want := range map[string]bool{
		"/m/cmd":         true,
		"/m/cmd/app":     true,
		"/m":             false,
		"/m/cmdline/app": false,
		"/other/cmd/app": false,
	} {
		if got := d.covers(filepath.FromSlash(dir)); got != want {
			t.Errorf("covers(%q) = %v, want %v", dir, got, want)
		}
	}
}

// TestVersionSkew: a fresh-or-stale verdict is a comparison with what
// this binary would generate, so doctor says first whether this binary is
// the servo the module builds against.
func TestVersionSkew(t *testing.T) {
	required := &packages.Module{Path: "github.com/okian/servo/v3", Version: "v3.5.0"}
	cases := []struct {
		name   string
		binary string
		mod    *packages.Module
		status string
		want   []string
	}{
		{"matching", "v3.5.0", required, "OK", []string{"servo v3.5.0, the version go.mod requires"}},
		{"skewed", "v3.4.1", required, "WARN", []string{"this servo is v3.4.1", "go.mod requires github.com/okian/servo/v3 v3.5.0", "go tool servo", "servoimpl.Contract1"}},
		{"devel binary", "", required, "INFO", []string{"development build", "v3.5.0"}},
		{"replaced runtime", "v3.5.0", &packages.Module{Path: "github.com/okian/servo/v3", Version: "v3.0.0", Replace: &packages.Module{Path: "../.."}}, "INFO", []string{"replaces github.com/okian/servo/v3 with ../.."}},
		{"servo itself", "", &packages.Module{Path: "github.com/okian/servo/v3", Main: true}, "INFO", []string{"servo's own module"}},
	}
	for _, c := range cases {
		got := versionSkew(c.binary, c.mod)
		if got.status != c.status {
			t.Errorf("%s: status %q, want %q (%s)", c.name, got.status, c.status, got.msg)
		}
		for _, want := range c.want {
			if !strings.Contains(got.msg, want) {
				t.Errorf("%s: message missing %q: %s", c.name, want, got.msg)
			}
		}
	}
	if got := versionSkew("v3.5.0", nil); got.status != "" {
		t.Errorf("no servo module to compare with should say nothing, got %+v", got)
	}
}

// TestWorkspaceModules: a go.work listing several modules makes the
// injector's own module a choice, which doctor names; GOWORK=off, the
// way CI usually runs, has none.
func TestWorkspaceModules(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "go.work", "go 1.23\n\nuse (\n\t./a\n\t./b\n)\n")
	mustWriteFile(t, root, "a/go.mod", "module example.com/a\n\ngo 1.23\n")
	mustWriteFile(t, root, "b/go.mod", "module example.com/b\n\ngo 1.23\n")

	gowork, uses, err := workspaceModules(filepath.Join(root, "a"))
	if err != nil {
		t.Fatalf("workspaceModules: %v", err)
	}
	if filepath.Base(gowork) != "go.work" || len(uses) != 2 {
		t.Errorf("workspaceModules = %q, %v; want the go.work and its two modules", gowork, uses)
	}

	t.Setenv("GOWORK", "off")
	if gowork, uses, err := workspaceModules(filepath.Join(root, "a")); err != nil || gowork != "" || uses != nil {
		t.Errorf("with GOWORK=off: %q, %v, %v; want no workspace", gowork, uses, err)
	}
}

// TestDoctorReportsAnOrphanedConfigLoader: a //servo:config type moved to
// another package leaves its generated loader behind, compiled into every
// build and naming a type no longer there — the search check makes, one
// FAIL per file.
func TestDoctorReportsAnOrphanedConfigLoader(t *testing.T) {
	const module = "example.com/doctormovedcfg"
	dir := writeMovableConfigModule(t, module)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	old := filepath.Join(dir, "cfgold", "servo_config_gen.go")
	if err := os.Remove(filepath.Join(dir, "cfgold", "config.go")); err != nil {
		t.Fatal(err)
	}
	writeConfigPackage(t, dir, "cfgnew")
	mustWriteFile(t, dir, "svc/svc.go", "package svc\n\nimport \""+module+"/cfgnew\"\n\ntype Service struct{}\n\nfunc New(c cfgnew.Config) *Service { return &Service{} }\n")

	out, err := doctorOutput(t, dir)
	if err == nil || !strings.Contains(out, "[FAIL] "+old+" is left over from a //servo:config type its package no longer declares") {
		t.Fatalf("doctor err=%v, want the orphaned loader named:\n%s", err, out)
	}
}

// TestDoctorCompilesTheFilesItFindsCurrent: a file identical to a fresh
// generation can still fail go build — here because its provider is
// declared in a servoinject-gated file, visible to the load and absent
// from the build — and doctor called it fresh.
func TestDoctorCompilesTheFilesItFindsCurrent(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/doctorcompiles\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	mustWriteFile(t, dir, "pkg/pkg.go", "package pkg\n\ntype T struct{}\n")
	mustWriteFile(t, dir, "pkg/new_inject.go", "//go:build servoinject\n\npackage pkg\n\nfunc New() *T { return &T{} }\n")
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nimport \"context\"\n\nfunc main() { _, _ = New(context.Background()) }\n")
	mustWriteFile(t, dir, "cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\"example.com/doctorcompiles/pkg\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() { servo.Build(servo.Root[*pkg.T]()) }\n")
	runGoModTidy(t, dir)
	_ = runGenerate(cfg(dir)) // writes the file, then reports that it does not compile

	out, err := doctorOutput(t, dir)
	if err == nil {
		t.Fatalf("doctor passed a generated file that does not compile:\n%s", out)
	}
	for _, want := range []string{"[OK  ] generated file matches a fresh generation", "the generated code does not compile", "pkg.New"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// TestDoctorRepeatsTheFlagsItWasGiven: "run `servo generate`" is wrong
// advice from anywhere but the directory doctor ran in, so the commands
// it suggests carry the --dir it was given.
func TestDoctorRepeatsTheFlagsItWasGiven(t *testing.T) {
	dir := writeAppModule(t, "example.com/doctorinvocation", true, "")
	var err error
	out := captureStdout(t, func() { err = run([]string{"doctor", "--dir", dir}) })
	if err == nil || !strings.Contains(out, "(run `servo generate --dir="+dir+"`)") {
		t.Fatalf("doctor --dir err=%v, want the suggestion to repeat --dir:\n%s", err, out)
	}
}
