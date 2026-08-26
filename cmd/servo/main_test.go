package main

import (
	"strings"
	"testing"
)

func TestRunDefaultsToGenerateWithNoArgs(t *testing.T) {
	dir := writeAppModule(t, "example.com/rundefault", true, "")
	t.Chdir(dir)

	if err := run(nil); err != nil {
		t.Fatalf("run(nil): %v", err)
	}
}

func TestRunDefaultsToGenerateWhenFirstArgIsAFlag(t *testing.T) {
	dir := writeAppModule(t, "example.com/rundefaultflag", true, "")

	// args[0] is "--dir", which starts with '-', so cmd stays "generate"
	// and the flag set still sees the full argument list.
	if err := run([]string{"--dir", dir}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunDispatchesCheck(t *testing.T) {
	dir := writeAppModule(t, "example.com/runcheck", true, "")
	if err := run([]string{"generate", "--dir", dir}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := run([]string{"check", "--dir", dir}); err != nil {
		t.Fatalf("check: %v", err)
	}
}

func TestRunDispatchesGraph(t *testing.T) {
	dir := writeAppModule(t, "example.com/rungraph", true, "")
	out := captureStdout(t, func() {
		if err := run([]string{"graph", "--dir", dir, "--format", "json"}); err != nil {
			t.Fatalf("graph: %v", err)
		}
	})
	if !strings.Contains(out, `"nodes"`) {
		t.Errorf("expected JSON graph output, got:\n%s", out)
	}
}

func TestRunDispatchesExplainAndWhy(t *testing.T) {
	dir := writeAppModule(t, "example.com/runexplainwhy", true, "")

	if err := run([]string{"explain", "--dir", dir, "api.Server"}); err != nil {
		t.Fatalf("explain: %v", err)
	}
	if err := run([]string{"why", "--dir", dir, "api.Server"}); err != nil {
		t.Fatalf("why: %v", err)
	}
}

func TestRunExplainRequiresExactlyOneArg(t *testing.T) {
	dir := writeAppModule(t, "example.com/runexplainusage", true, "")
	err := run([]string{"explain", "--dir", dir})
	if err == nil || !strings.Contains(err.Error(), "usage: servo explain") {
		t.Fatalf("got err=%v, want a usage error", err)
	}
}

func TestRunWhyRequiresExactlyOneArg(t *testing.T) {
	dir := writeAppModule(t, "example.com/runwhyusage", true, "")
	err := run([]string{"why", "--dir", dir})
	if err == nil || !strings.Contains(err.Error(), "usage: servo why") {
		t.Fatalf("got err=%v, want a usage error", err)
	}
}

func TestRunDispatchesList(t *testing.T) {
	dir := writeAppModule(t, "example.com/runlist", true, "")
	if err := run([]string{"list", "--dir", dir}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestRunDispatchesInit(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"init", "--dir", dir}); err != nil {
		t.Fatalf("init: %v", err)
	}
}

func TestRunDispatchesDoctor(t *testing.T) {
	dir := writeAppModule(t, "example.com/rundoctor", true, "")
	// The fixture has no generated file yet, so doctor reports a problem —
	// dispatch itself is what's under test here, not a clean bill of health.
	_ = run([]string{"doctor", "--dir", dir})
}

func TestRunDispatchesMigrate(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "app.go", "package app\n\nfunc setup() { Register(&X{}, 1) }\n")
	if err := run([]string{"migrate", "--dir", dir}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestRunDispatchesNew(t *testing.T) {
	if err := run([]string{"new", "component", "Widget"}); err != nil {
		t.Fatalf("new: %v", err)
	}
}

func TestRunNewRequiresAtLeastOneArg(t *testing.T) {
	err := run([]string{"new"})
	if err == nil || !strings.Contains(err.Error(), "usage: servo new") {
		t.Fatalf("got err=%v, want a usage error", err)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	err := run([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("got err=%v, want an 'unknown command' error", err)
	}
}

func TestRunFlagParseError(t *testing.T) {
	err := run([]string{"generate", "--not-a-real-flag"})
	if err == nil {
		t.Fatal("expected a flag-parse error for an unrecognized flag")
	}
}

func TestBuildPipelineReportsNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/brokensibling", true, "")
	// A sibling package with a real compile error, unrelated to the
	// injector itself, must surface through buildPipeline's own
	// NonInjectorErrors check rather than being silently ignored.
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	err := runExplain(dir, "api.Server", false)
	if err == nil || !strings.Contains(err.Error(), "module has build errors") {
		t.Fatalf("got err=%v, want a 'module has build errors' error", err)
	}
}

func TestBuildPipelinesReportsNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/brokensibling2", true, "")
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	err := runGenerate(dir)
	if err == nil || !strings.Contains(err.Error(), "module has build errors") {
		t.Fatalf("got err=%v, want a 'module has build errors' error", err)
	}
}
