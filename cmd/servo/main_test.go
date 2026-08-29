package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"runtime"
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
	if !strings.Contains(err.Error(), "mock-adapter") {
		t.Errorf("got err=%v, want the usage string to mention mock-adapter too", err)
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

// TestRunFlagParseErrorsForEverySubcommand covers every subcommand's own
// flag.FlagSet.Parse error branch individually: each case in run's switch
// constructs its own FlagSet, so TestRunFlagParseError above (generate
// only) leaves the other eight as separate, untested basic blocks.
func TestRunFlagParseErrorsForEverySubcommand(t *testing.T) {
	for _, cmd := range []string{"generate", "check", "graph", "explain", "why", "list", "init", "doctor", "migrate"} {
		t.Run(cmd, func(t *testing.T) {
			if err := run([]string{cmd, "--not-a-real-flag"}); err == nil {
				t.Fatalf("run(%q, --not-a-real-flag) = nil error, want a flag-parse error", cmd)
			}
		})
	}
}

func TestBuildPipelineReportsNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/brokensibling", true, "")
	// A sibling package with a real compile error, unrelated to the
	// injector itself, must surface through buildPipeline's own
	// NonInjectorErrors check rather than being silently ignored.
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	err := runExplain(cfg(dir), "api.Server", false)
	if err == nil || !strings.Contains(err.Error(), "module has build errors") {
		t.Fatalf("got err=%v, want a 'module has build errors' error", err)
	}
}

func TestBuildPipelinesReportsNonInjectorBuildErrors(t *testing.T) {
	dir := writeAppModule(t, "example.com/brokensibling2", true, "")
	mustWriteFile(t, dir, "broken/broken.go", "package broken\n\nfunc Bad() int { return \"not an int\" }\n")

	err := runGenerate(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "module has build errors") {
		t.Fatalf("got err=%v, want a 'module has build errors' error", err)
	}
}

// TestBuildPipelineForwardsLoadModuleError and
// TestBuildPipelinesForwardsLoadModuleError cover buildPipeline/
// buildPipelines' own error-forwarding branch for loadModule failing —
// distinct from every other test that reaches loadModule failure only
// indirectly through a caller like runCheck/runList.
func TestBuildPipelineForwardsLoadModuleError(t *testing.T) {
	_, err := buildPipeline(cfg(filepath.Join(t.TempDir(), "does-not-exist")))
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

func TestBuildPipelinesForwardsLoadModuleError(t *testing.T) {
	_, err := buildPipelines(cfg(filepath.Join(t.TempDir(), "does-not-exist")))
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

// TestBuildPipelineForwardsFindSpecError and
// TestBuildPipelinesForwardsFindSpecsError cover the FindSpec(s)
// error-forwarding branch: a real, loadable module that never calls
// servo.Build(...) at all.
func TestBuildPipelineForwardsFindSpecError(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/nospecpipeline\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nimport _ \"github.com/okian/servo/v3/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	_, err := buildPipeline(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "no servo.Build") {
		t.Fatalf("got err=%v, want a 'no servo.Build' error", err)
	}
}

func TestBuildPipelinesForwardsFindSpecsError(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/nospecpipelines\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "main.go", "package main\n\nimport _ \"github.com/okian/servo/v3/servo\"\n\nfunc main() {}\n")
	runGoModTidy(t, dir)

	_, err := buildPipelines(cfg(dir))
	if err == nil || !strings.Contains(err.Error(), "no servo.Build") {
		t.Fatalf("got err=%v, want a 'no servo.Build' error", err)
	}
}

// TestRunPrintsUsageForEveryHelpSpelling. The dispatch below it leaves a
// leading flag with the default command, so a `-h` check placed after the
// extraction can never fire — which is how `servo -h` printed generate's
// four flags instead of the command list, in the one place a new user looks
// for the other nine commands.
func TestRunPrintsUsageForEveryHelpSpelling(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help", "-help"} {
		t.Run(arg, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() { err = run([]string{arg}) })
			if err != nil {
				t.Fatalf("run(%q) = %v, want nil", arg, err)
			}
			for _, want := range []string{"usage: servo <command>", "generate", "doctor", "version", "--overlay"} {
				if !strings.Contains(out, want) {
					t.Errorf("usage for %q does not mention %q:\n%s", arg, want, out)
				}
			}
		})
	}
}

// TestRunHelpForOneCommand and its unknown-topic twin: the list is the
// fallback whenever a name does not resolve, since being told a name is
// wrong without being shown the right ones is a dead end.
func TestRunHelpForOneCommand(t *testing.T) {
	var err error
	out := captureStdout(t, func() { err = run([]string{"help", "check"}) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "usage: servo check") {
		t.Errorf("help check did not print check's usage:\n%s", out)
	}
}

func TestRunHelpForAnUnknownCommandListsTheRealOnes(t *testing.T) {
	err := run([]string{"help", "geneate"})
	if err == nil {
		t.Fatal("run = nil, want an error for an unknown topic")
	}
	if !strings.Contains(err.Error(), "generate") {
		t.Errorf("the error does not list the real commands:\n%v", err)
	}
}

func TestRunUnknownCommandListsTheRealOnes(t *testing.T) {
	err := run([]string{"geneate"})
	if err == nil {
		t.Fatal("run = nil, want an unknown-command error")
	}
	for _, want := range []string{`unknown command "geneate"`, "generate", "servo help"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

func TestRunVersionPrintsTheBuildsVersion(t *testing.T) {
	var err error
	out := captureStdout(t, func() { err = run([]string{"version"}) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(out, "servo ") || !strings.Contains(out, runtime.Version()) {
		t.Errorf("version output %q does not name the servo and Go versions", out)
	}
}

// TestPathFlagsAreResolvedAgainstTheCallerNotAgainstDir pins the one place
// servo's build flags deliberately do more than record what was typed. The
// loader runs the go command with its Dir set to --dir, so a --modfile or
// --overlay left relative would be looked up inside the module being
// scanned rather than next to the person who typed it: `servo check --dir
// cmd/app --overlay=overlay.json` would fail on an overlay.json sitting in
// the caller's own directory, which is not how the same flag behaves on
// `go build`. Resolution therefore has to happen at parse time, before the
// value reaches load.BuildFlags.
func TestPathFlagsAreResolvedAgainstTheCallerNotAgainstDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "overlay.json")

	cases := []struct {
		name string
		flag string
		args []string
		want string
	}{
		{
			// The case the flag type exists for at all.
			name: "a relative --overlay is anchored to the working directory",
			flag: "overlay",
			args: []string{"--overlay", "overlay.json"},
			want: filepath.Join(cwd, "overlay.json"),
		},
		{
			// --modfile is the same type, and a divergence between the two
			// would be the kind of inconsistency nobody reads the code to
			// discover.
			name: "a relative --modfile is anchored the same way",
			flag: "modfile",
			args: []string{"--modfile", filepath.Join("sub", "go.mod")},
			want: filepath.Join(cwd, "sub", "go.mod"),
		},
		{
			// An already-absolute path names a file outside the working
			// directory on purpose; rewriting it would point the go command
			// somewhere else entirely.
			name: "an absolute path is left exactly as typed",
			flag: "overlay",
			args: []string{"--overlay", elsewhere},
			want: elsewhere,
		},
		{
			// Empty means "not set". filepath.Abs("") is the working
			// directory, so resolving it would hand the go command a
			// directory where it expects an overlay file.
			name: "an explicitly empty value stays empty rather than becoming the cwd",
			flag: "overlay",
			args: []string{"--overlay="},
			want: "",
		},
		{
			name: "an unset flag stays empty",
			flag: "overlay",
			args: nil,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := flag.NewFlagSet("generate", flag.ContinueOnError)
			build := registerBuildFlags(fs)
			if err := fs.Parse(c.args); err != nil {
				t.Fatalf("Parse(%v): %v", c.args, err)
			}

			got := build.Overlay
			if c.flag == "modfile" {
				got = build.ModFile
			}
			if got != c.want {
				t.Errorf("--%s reached load.BuildFlags as %q, want %q", c.flag, got, c.want)
			}
			// String is what the flag package reads the value back through
			// (usage text, error messages); reporting the raw argument here
			// while the loader gets the resolved one would make every
			// diagnostic about a path problem name a path that was not used.
			if got := fs.Lookup(c.flag).Value.String(); got != c.want {
				t.Errorf("Lookup(%q).String() = %q, want %q", c.flag, got, c.want)
			}
		})
	}
}

// TestPathFlagUsageDoesNotReportAPanic covers pathFlag.String's nil-target
// guard, which is not defensive padding: the flag package constructs a
// zero pathFlag by reflection to decide whether a default is worth
// printing, and calls String on it. Without the guard that call
// dereferences a nil pointer, and `servo generate --help` prints "panic
// calling String method" into the middle of its own usage text — in the
// one place a confused user is already looking for help.
func TestPathFlagUsageDoesNotReportAPanic(t *testing.T) {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	registerBuildFlags(fs)
	var out bytes.Buffer
	fs.SetOutput(&out)

	fs.PrintDefaults()

	if strings.Contains(out.String(), "panic calling String method") {
		t.Errorf("usage text carries a panic report from a zero-valued path flag:\n%s", out.String())
	}
	for _, want := range []string{"-modfile", "-overlay"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage text does not document %s, so the assertion above proves nothing:\n%s", want, out.String())
		}
	}
}
