package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// These cover how generate, check and config fail: what each reports
// when the module, the graph, or the filesystem gets in the way, and that
// the report names the injector or file involved.

// TestGeneratePrintsTheResolversNotes: a Close that no Stop bridges to
// never stops generation, but it is said, on stderr, every run.
func TestGeneratePrintsTheResolversNotes(t *testing.T) {
	const module = "example.com/gennotes"
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module "+module+"\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	mustWriteFile(t, dir, "pool/pool.go", "package pool\n\ntype Pool struct{}\n\nfunc New() *Pool { return &Pool{} }\n\nfunc (p *Pool) Close() error { return nil }\n")
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\""+module+"/pool\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(servo.Root[*pool.Pool]())\n}\n")
	runGoModTidy(t, dir)

	var err error
	stderr := captureStderr(t, func() { err = runGenerate(cfg(dir)) })
	if err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if want := "servo: note: *" + module + "/pool.Pool has a Close method but no Stop(ctx context.Context) error, so servo will not release it at shutdown"; !strings.Contains(stderr, want) {
		t.Errorf("generate's stderr missing %q:\n%s", want, stderr)
	}
}

// TestGenerateNamesTheInjectorWhoseFileItCannotWrite: a write that fails
// is reported against its injector, and the run fails.
func TestGenerateNamesTheInjectorWhoseFileItCannotWrite(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	dir := writeAppModule(t, "example.com/genunwritable", true, "")
	appDir := filepath.Join(dir, "cmd", "app")
	if err := os.Chmod(appDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(appDir, 0o755) })

	err := runGenerate(cfg(dir))
	if err == nil || !strings.HasPrefix(err.Error(), "example.com/genunwritable/cmd/app: ") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("runGenerate = %v, want the write failure under the injector's name", err)
	}
}

// TestFileWriterReportsARemovalItCouldNotMake: a removal that fails is
// returned, and not logged as done.
func TestFileWriterReportsARemovalItCouldNotMake(t *testing.T) {
	var log bytes.Buffer
	w := newFileWriter(&log, true)
	if err := w.remove(filepath.Join(t.TempDir(), "gone.go"), "no reason"); err == nil {
		t.Fatal("remove of a missing file = nil error")
	}
	if log.Len() != 0 {
		t.Errorf("a failed removal was logged: %q", log.String())
	}
}

// TestOrphanedCompanionsSearchBoundaries: no pipelines means nothing to
// search, a module root that cannot be read is an error, and an
// unreadable directory below the root is passed over.
func TestOrphanedCompanionsSearchBoundaries(t *testing.T) {
	if got, err := orphanedCompanions(nil); got != nil || err != nil {
		t.Errorf("orphanedCompanions(nil) = %v, %v; want nothing", got, err)
	}

	moduleAt := func(dir string) []*pipeline {
		pkg := &packages.Package{PkgPath: "example.com/m", Module: &packages.Module{Dir: dir}}
		return []*pipeline{{
			loaded: &load.Loaded{ByPath: map[string]*packages.Package{pkg.PkgPath: pkg}, All: []*packages.Package{pkg}},
			scope:  map[string]bool{pkg.PkgPath: true},
		}}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := orphanedCompanions(moduleAt(missing)); err == nil || !strings.HasPrefix(err.Error(), "servo: looking for left-over config loaders: ") {
		t.Errorf("orphanedCompanions over a missing module root = %v, want the search failure", err)
	}

	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		return
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if got, err := orphanedCompanions(moduleAt(root)); err != nil || len(got) != 0 {
		t.Errorf("orphanedCompanions past an unreadable directory = %v, %v; want nothing and no error", got, err)
	}
	if !holdsOtherGoFiles(locked) {
		t.Error("holdsOtherGoFiles on an unreadable directory = false; it cannot tell, so it must not call the loader an orphan")
	}
}

// TestCheckNamesBothSidesOfAConfigDisagreement: two injectors share a
// config, and the second one in package order is the one declaring
// servo.ConfigFile; check reports the disagreement with each injector on
// the right side of it, and keeps going.
func TestCheckNamesBothSidesOfAConfigDisagreement(t *testing.T) {
	const module = "example.com/checkcfgmix"
	dir := writeConfigModule(t, module, false)
	mustWriteFile(t, dir, "cmd/worker/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\""+module+"/store\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(\n\t\tservo.Root[*store.Store](),\n\t\tservo.ConfigFile(\"config.json\"),\n\t)\n}\n")
	mustWriteFile(t, dir, "cmd/worker/main.go", "package main\n\nimport \"context\"\n\nfunc main() { _, _ = New(context.Background()) }\n")

	err := runCheck(cfg(dir))
	want := "is used by " + module + "/cmd/worker (which declares servo.ConfigFile) and by " + module + "/cmd/app (which does not)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("runCheck = %v, want the disagreement stated as %q", err, want)
	}
	// It kept checking: neither injector has been generated.
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("check stopped at the disagreement instead of checking every injector: %v", err)
	}
}

// TestConfigAgreementSkipsAnInjectorThatDidNotResolve: an injector whose
// graph failed is reported once, for its own failure; its config usage
// cannot be known, so it takes no part in the agreement check.
func TestConfigAgreementSkipsAnInjectorThatDidNotResolve(t *testing.T) {
	const module = "example.com/checkcfgbroken"
	dir := writeConfigModule(t, module, true)
	mustWriteFile(t, dir, "ghost/ghost.go", "package ghost\n\ntype Ghost struct{}\n")
	mustWriteFile(t, dir, "cmd/worker/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\""+module+"/ghost\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(servo.Root[*ghost.Ghost]())\n}\n")
	mustWriteFile(t, dir, "cmd/worker/main.go", "package main\n\nfunc main() {}\n")

	err := runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), module+"/cmd/worker: ") || !strings.Contains(err.Error(), "no provider for *"+module+"/ghost.Ghost") {
		t.Fatalf("runCheck = %v, want cmd/worker's resolution failure", err)
	}
	if strings.Contains(err.Error(), "declare a ConfigFile in both injectors or neither") {
		t.Errorf("an injector that did not resolve took part in the agreement check: %v", err)
	}
}

// TestCheckPipelineReportsAResolutionFailure: the single-injector check
// stops at a graph that does not resolve, with the resolver's diagnostic.
func TestCheckPipelineReportsAResolutionFailure(t *testing.T) {
	dir := writeAppModule(t, "example.com/checkpipeambiguous", false, "")
	p, err := buildPipeline(cfg(dir))
	if err != nil {
		t.Fatalf("buildPipeline: %v", err)
	}
	if err := checkPipeline(p); err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Fatalf("checkPipeline = %v, want the ambiguity", err)
	}
}

// TestOverrideVariantEmitFailureSaysWhichGraph: when the NewTestApp
// graph resolves but cannot be emitted, check says the failure is in that
// graph. As in TestCheckOneFailsWhenEmitFails, an illegal package name is
// the only way to make emit fail.
func TestOverrideVariantEmitFailureSaysWhichGraph(t *testing.T) {
	spec := &load.Spec{
		InjectorPkg: &packages.Package{Name: "type", PkgPath: "example.com/badvariant"},
		Overrides:   []load.BindDecl{{Iface: graph.Key{Type: "example.com/badvariant.Store"}, Concrete: graph.Key{Type: "*example.com/badvariant.Fake"}}},
	}
	p := &pipeline{spec: spec, caps: graph.EmptyCapabilities(), scope: map[string]bool{}}
	err := newChecker(invocation{}).checkTestVariant(p)
	if err == nil || !strings.HasPrefix(err.Error(), "in the NewTestApp graph (servo.Override at ") || !strings.Contains(err.Error(), "failed to format") {
		t.Errorf("checkTestVariant = %v, want the emit failure, labelled as the test graph's", err)
	}
}

// TestBuildPipelinesReportsAMalformedConfigDirective: //servo:config's
// options are checked as the module loads, so a bad prefix fails every
// command at once, naming the directive.
func TestBuildPipelinesReportsAMalformedConfigDirective(t *testing.T) {
	dir := writeConfigModule(t, "example.com/badcfgdirective", false)
	src, err := os.ReadFile(filepath.Join(dir, "store", "store.go"))
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, dir, "store/store.go", strings.Replace(string(src), "//servo:config prefix=APP", "//servo:config prefix=app", 1))
	err = runCheck(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), `//servo:config prefix "app" must be UPPER_SNAKE`) || !strings.Contains(err.Error(), "store.go") {
		t.Fatalf("runCheck = %v, want the malformed directive named with its position", err)
	}
}

// TestBuildPipelinesChecksEveryInjectorInTheModule: --dir narrows which
// injectors are generated, not which specs the module-wide checks read,
// so a spec that does not parse anywhere in the module fails the run.
func TestBuildPipelinesChecksEveryInjectorInTheModule(t *testing.T) {
	const module = "example.com/brokenspecsibling"
	dir := writeAppModule(t, module, true, "")
	mustWriteFile(t, dir, "cmd/other/main.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "cmd/other/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\""+module+"/api\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(servo.Root[*api.Server](), servo.Root[*api.Server](), servo.Value[int, int]())\n}\n")
	err := runGenerate(cfg(filepath.Join(dir, "cmd", "app")))
	if err == nil || !strings.Contains(err.Error(), filepath.Join("cmd", "other", "spec.go")) {
		t.Fatalf("runGenerate --dir cmd/app = %v, want cmd/other's broken spec reported", err)
	}
}

// TestRunConfigReportsEveryOutcome: the manual as JSON, a graph with no
// config types, and the two ways there is no graph to read.
func TestRunConfigReportsEveryOutcome(t *testing.T) {
	dir := writeConfigModule(t, "example.com/configjson", false)
	var err error
	out := captureStdout(t, func() { err = runConfig(cfg(dir), true) })
	if err != nil {
		t.Fatalf("runConfig --json: %v", err)
	}
	for _, want := range []string{`"env": "APP_DSN"`, `"required": true`, `"env": "APP_MAX_CONNS"`, `"default": "10"`, `"type": "int32"`, `"secret": true`} {
		if !strings.Contains(out, want) {
			t.Errorf("config --json missing %s:\n%s", want, out)
		}
	}

	plain := writeAppModule(t, "example.com/confignone", true, "")
	out = captureStdout(t, func() { err = runConfig(cfg(plain), false) })
	if err != nil || !strings.HasSuffix(out, "  no //servo:config types are in this graph\n") {
		t.Errorf("runConfig on a graph without configs = %v:\n%s", err, out)
	}

	if err := runConfig(cfg(filepath.Join(t.TempDir(), "missing")), false); err == nil {
		t.Error("runConfig on a missing directory = nil error")
	}
	ambiguous := writeAppModule(t, "example.com/configambiguous", false, "")
	if err := runConfig(cfg(ambiguous), false); err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Errorf("runConfig on an unresolvable graph = %v, want the ambiguity", err)
	}
}

// TestGenerateReportsAnOrphanItCannotRemove: an orphaned companion in a
// directory generate may not change is reported as a failure, and not as
// removed.
func TestGenerateReportsAnOrphanItCannotRemove(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a directory the test cannot write to")
	}
	const module = "example.com/stuckorphan"
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
	locked := filepath.Dir(old)
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	var err error
	stderr := captureStderr(t, func() { err = runGenerate(cfg(dir)) })
	if err == nil || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("generate = %v, want the failed removal of %s", err, old)
	}
	if strings.Contains(stderr, "removed "+old) {
		t.Errorf("a removal that failed was reported as done:\n%s", stderr)
	}
}

// TestInjectorsThatAgreeShareOneConfigLoader: two injectors using the same
// config without a ConfigFile agree, so generate writes its loader once
// and check finds both injectors current.
func TestInjectorsThatAgreeShareOneConfigLoader(t *testing.T) {
	const module = "example.com/cfgagree"
	dir := writeConfigModule(t, module, false)
	mustWriteFile(t, dir, "cmd/worker/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\""+module+"/store\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(servo.Root[*store.Store]())\n}\n")
	mustWriteFile(t, dir, "cmd/worker/main.go", "package main\n\nimport \"context\"\n\nfunc main() { _, _ = New(context.Background()) }\n")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("runCheck after generating two agreeing injectors: %v", err)
	}
	for _, path := range []string{"cmd/app/servo_gen.go", "cmd/worker/servo_gen.go", "store/servo_config_gen.go"} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("generate did not write %s: %v", path, err)
		}
	}
}
