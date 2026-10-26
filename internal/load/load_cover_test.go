package load

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// TestLoadRefusesAnUnknownScope: --scope is checked before the go command
// is asked anything, so a mistyped value is named instead of being read as
// the default.
func TestLoadRefusesAnUnknownScope(t *testing.T) {
	_, err := Load(Config{Dir: t.TempDir(), Scope: "modules"})
	if err == nil || !strings.Contains(err.Error(), "--scope=modules") {
		t.Fatalf("Load(Scope: modules) = %v, want the scope refused", err)
	}
}

// TestCheckDirLeavesTheRestToTheSystem covers the two cases
// TestLoadReturnsErrorForInvalidDir does not: no Dir at all, which is the
// current directory and needs no check, and a path the system refuses to
// stat for a reason other than absence — one running through a regular
// file — which is passed on as the system put it, not called missing.
func TestCheckDirLeavesTheRestToTheSystem(t *testing.T) {
	if err := checkDir(""); err != nil {
		t.Errorf(`checkDir("") = %v, want nil`, err)
	}
	if runtime.GOOS == "windows" {
		return // Windows reports a path through a file as one that does not exist.
	}
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/x\n")
	err := checkDir(filepath.Join(dir, "go.mod", "cmd"))
	if err == nil || !strings.HasPrefix(err.Error(), "servo: stat ") || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("checkDir(a path through a file) = %v, want the stat error itself", err)
	}
}

// TestPatternErrorKeepsAnUnknownRefusalVerbatim: a refusal of the ./...
// pattern servo has no advice for is passed on in the go command's own
// words, and an empty Dir is named as the current directory.
func TestPatternErrorKeepsAnUnknownRefusalVerbatim(t *testing.T) {
	const msg = "pattern ./...: directory prefix . outside main module or its selected dependencies"
	if err := patternError(".", msg); err == nil || err.Error() != "load: "+msg {
		t.Errorf("patternError = %v, want %q", err, "load: "+msg)
	}
	if got := displayDir(""); got != "." {
		t.Errorf(`displayDir("") = %q, want "."`, got)
	}
}

// TestLoadReportsAGoCommandThatWillNotRun: a go command that refuses to
// run at all — here a GOFLAGS entry that is not a flag — is reported in its
// own words from the first question servo asks it, rather than read as a
// directory holding no packages.
func TestLoadReportsAGoCommandThatWillNotRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOFLAGS", "bogus")
	_, err := Load(Config{Dir: dir})
	if err == nil || !strings.HasPrefix(err.Error(), "load: ") || !strings.Contains(err.Error(), `parsing $GOFLAGS: non-flag "bogus"`) {
		t.Fatalf("Load = %v, want the go command's own refusal", err)
	}
}

// TestLoadRefusesPackagesOfNoMainModule: inside the Go distribution's own
// source tree, ./... matches packages that belong to no main module, so
// there is no user code to generate for — said as such, rather than as a
// servo runtime nothing imports.
func TestLoadRefusesPackagesOfNoMainModule(t *testing.T) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	dir := filepath.Join(strings.TrimSpace(string(out)), "src", "container")
	_, err = Load(Config{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "belong to no main module — run servo from inside your own module") {
		t.Fatalf("Load(%s) = %v, want the packages called no main module's", dir, err)
	}
}

// fakeGoFailing puts a stand-in go command first on PATH. It fails — "go:
// simulated failure", exit 1 — when invoked with every one of args, and
// runs the real go command otherwise: a go command that dies partway
// through a load, which no real module can be made to do on cue.
func fakeGoFailing(t *testing.T, args ...string) {
	t.Helper()
	fakeGo(t, "echo 'go: simulated failure' >&2\nexit 1\n", args...)
}

// fakeGo is fakeGoFailing with the failure written by the caller: onMatch
// is the shell run when every one of args is present, with the real go
// command in $real and a scratch directory in $scratch.
func fakeGo(t *testing.T, onMatch string, args ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in go command is a shell script")
	}
	real, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("finding the go command: %v", err)
	}
	bin := t.TempDir()
	var script strings.Builder
	fmt.Fprintf(&script, "#!/bin/sh\nreal='%s'\nscratch='%s'\n", real, t.TempDir())
	for _, arg := range args {
		fmt.Fprintf(&script, "case \" $* \" in *\" %s \"*) ;; *) exec \"$real\" \"$@\" ;; esac\n", arg)
	}
	script.WriteString(onMatch)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestLoadReportsAGoCommandThatFailsPartway covers the go/packages calls
// after the first. The import graph is asked for on its own, and its
// failure is reported rather than taken for an empty module. So is a
// failure of the type-checked load, which go/packages itself does not
// report: the go command's own words, its exit status when it printed
// none, and an empty load even when asking again succeeds. When export
// data cannot be read and the from-source retry fails as well, the error
// names both: the decode failure that forced the retry, with its remedy,
// and why the retry could not stand in for it.
func TestLoadReportsAGoCommandThatFailsPartway(t *testing.T) {
	dir := writeFixtureModule(t, "")
	t.Run("the import graph", func(t *testing.T) {
		// -deps without -compiled is the import-graph call, and only it.
		fakeGoFailing(t, "-deps=true", "-compiled=false")
		_, err := Load(Config{Dir: dir})
		if err == nil || !strings.HasPrefix(err.Error(), "load: ") || !strings.Contains(err.Error(), "go: simulated failure") {
			t.Fatalf("Load = %v, want the go command's failure", err)
		}
	})
	t.Run("the type-checked load", func(t *testing.T) {
		// -export is the call that type-checks against export data, and
		// go/packages reports nothing when it fails: what reaches servo is
		// a load with no packages in it, which read as a missing runtime.
		fakeGoFailing(t, "-export=true")
		_, err := Load(Config{Dir: dir})
		if err == nil || err.Error() != "load: go list -export failed: go: simulated failure" {
			t.Fatalf("Load = %v, want the go command's failure", err)
		}
	})
	t.Run("the type-checked load, without a word", func(t *testing.T) {
		fakeGo(t, "exit 3\n", "-export=true")
		_, err := Load(Config{Dir: dir})
		if err == nil || err.Error() != "load: go list -export failed: exit status 3" {
			t.Fatalf("Load = %v, want the go command's exit status", err)
		}
	})
	t.Run("the type-checked load, once", func(t *testing.T) {
		// Failing the first time only, the way a transient failure does:
		// asked again, the go command lists the packages after all.
		fakeGo(t, "if [ -e \"$scratch/failed\" ]; then exec \"$real\" \"$@\"; fi\ntouch \"$scratch/failed\"\nexit 1\n", "-export=true")
		_, err := Load(Config{Dir: dir})
		if err == nil || !strings.Contains(err.Error(), "listed no packages, then listed them when asked again — run servo again") {
			t.Fatalf("Load = %v, want the empty load reported", err)
		}
	})
	t.Run("the from-source retry", func(t *testing.T) {
		failExportData(t)
		// -compiled without -export is the retry's call, and only it.
		fakeGoFailing(t, "-compiled=true", "-export=false")
		_, err := Load(Config{Dir: dir})
		var unreadable *exportDataError
		if !errors.As(err, &unreadable) {
			t.Fatalf("Load = %v, want the export data failure reported", err)
		}
		for _, want := range []string{
			"cannot read the export data",
			"export data version 5 is greater than maximum supported version 4",
			"type-checking the standard library from source instead failed too: load: ",
			"go: simulated failure",
			"go get golang.org/x/tools@latest",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Load = %v\nwant it to contain %q", err, want)
			}
		}
	})
}

// TestRuntimeLoadErrorNamesTheFix covers the go command's other reasons the
// runtime did not load: a missing go.sum entry has its own fix, and a
// reason servo has no advice for is passed on whole, folded onto one line,
// at the position go list gave or at none. Errors other than list errors —
// a type error inside the runtime — are not reasons it failed to load.
func TestRuntimeLoadErrorNamesTheFix(t *testing.T) {
	runtimePkg := func(errs ...packages.Error) *packages.Package {
		return &packages.Package{PkgPath: graph.ServoPackagePath, Errors: errs}
	}
	for _, c := range []struct {
		name string
		pkg  *packages.Package
		want string // "" for nil
	}{
		{
			name: "a missing go.sum entry",
			pkg:  runtimePkg(packages.Error{Kind: packages.ListError, Pos: "cmd/app/spec.go:5:2", Msg: "missing go.sum entry for module providing package github.com/okian/servo/v3/servo (imported by example.com/app/cmd/app); to add:\n\tgo get example.com/app/cmd/app"}),
			want: "servo: cmd/app/spec.go:5:2: go.sum has no entry for github.com/okian/servo/v3 (go: missing go.sum entry for module providing package github.com/okian/servo/v3/servo (imported by example.com/app/cmd/app); to add: go get example.com/app/cmd/app) — run `go mod tidy`",
		},
		{
			name: "any other reason",
			pkg:  runtimePkg(packages.Error{Kind: packages.ListError, Pos: "-", Msg: "github.com/okian/servo/v3@v3.9.9: reading go.mod:\n\tunexpected EOF"}),
			want: "servo: the servo runtime package github.com/okian/servo/v3/servo did not load: github.com/okian/servo/v3@v3.9.9: reading go.mod: unexpected EOF",
		},
		{
			name: "a type error",
			pkg:  runtimePkg(packages.Error{Kind: packages.TypeError, Pos: "servo.go:3:1", Msg: "undefined: x"}),
		},
		{name: "no error", pkg: runtimePkg()},
	} {
		err := runtimeLoadError(c.pkg)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: runtimeLoadError = %v, want nil", c.name, err)
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%s: runtimeLoadError = %v\nwant %s", c.name, err, c.want)
		}
	}
}

// TestRuntimeVersionNamesOnlyARelease: the version set beside the CLI's in
// a skew message is the release go.mod requires. A runtime replaced by a
// directory or another module, a development build, the module being servo
// itself, or no runtime package at all leave nothing to name.
func TestRuntimeVersionNamesOnlyARelease(t *testing.T) {
	withModule := func(m *packages.Module) *Loaded {
		return &Loaded{ServoPkg: &packages.Package{PkgPath: graph.ServoPackagePath, Module: m}}
	}
	for _, c := range []struct {
		name string
		l    *Loaded
		want string
	}{
		{"required", withModule(&packages.Module{Path: "github.com/okian/servo/v3", Version: "v3.4.1"}), "v3.4.1"},
		{"replaced", withModule(&packages.Module{Path: "github.com/okian/servo/v3", Version: "v3.4.1", Replace: &packages.Module{Path: "../servo"}}), ""},
		{"a development build", withModule(&packages.Module{Path: "github.com/okian/servo/v3", Version: "(devel)"}), ""},
		{"servo itself", withModule(&packages.Module{Path: "github.com/okian/servo/v3", Main: true}), ""},
		{"no module", withModule(nil), ""},
		{"no runtime", &Loaded{}, ""},
	} {
		if got := c.l.RuntimeVersion(); got != c.want {
			t.Errorf("%s: RuntimeVersion = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestReadExportDataLeavesThePackageOnFailure covers readExportData's three
// ways to fail. A file that will not open is an *fs.PathError, the build
// cache problem completeExportData reports as itself; a file that is not
// export data, and export data that does not decode, are the reader's own
// errors, the ones the from-source fallback exists for. None of them
// replaces the partial package the load already holds.
func TestReadExportDataLeavesThePackageOnFailure(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "notexport.a", "not an archive\n")
	// A leading 'i' is how the reader recognizes a bare export file, so
	// this one gets past the header and fails to decode.
	mustWriteFile(t, dir, "undecodable.x", "i\x00\xff\xfe not export data")
	for _, c := range []struct {
		name, file string
		pathError  bool
	}{
		{"missing", filepath.Join(dir, "missing.a"), true},
		{"not export data", filepath.Join(dir, "notexport.a"), false},
		{"undecodable", filepath.Join(dir, "undecodable.x"), false},
	} {
		partial := types.NewPackage("example.com/partial", "partial")
		p := &packages.Package{PkgPath: partial.Path(), ExportFile: c.file, Types: partial}
		err := readExportData(p, token.NewFileSet(), map[string]*types.Package{partial.Path(): partial})
		var pathErr *fs.PathError
		if err == nil || errors.As(err, &pathErr) != c.pathError {
			t.Errorf("%s: readExportData = %v, want an error that is a path error: %v", c.name, err, c.pathError)
		}
		if p.Types != partial {
			t.Errorf("%s: the partial package was replaced by a failed read", c.name)
		}
	}
}

// TestIsInjectorInAnotherConfigurationSkipsAnUnparseableFile: an excluded
// .go file that does not parse is no evidence either way. It neither makes
// its package an injector nor hides the spec file excluded beside it.
func TestIsInjectorInAnotherConfigurationSkipsAnUnparseableFile(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "broken.go", "//go:build ignore\n\npackage\n")
	mustWriteFile(t, dir, "spec_prod.go", "//go:build servoinject && prod\n\npackage main\n")
	broken, spec := filepath.Join(dir, "broken.go"), filepath.Join(dir, "spec_prod.go")
	if isInjectorInAnotherConfiguration(&packages.Package{IgnoredFiles: []string{broken}}) {
		t.Error("an unparseable excluded file made its package an injector")
	}
	if !isInjectorInAnotherConfiguration(&packages.Package{IgnoredFiles: []string{broken, spec}}) {
		t.Error("an unparseable excluded file hid the spec file excluded after it")
	}
}

// TestNonInjectorErrorsOnAHandBuiltLoad covers both ends of the check: a
// load whose packages have no errors has nothing to report, and a module
// package loaded without type information — so without the syntax a spec
// is found in — is never taken for an injector, so its errors stand.
func TestNonInjectorErrorsOnAHandBuiltLoad(t *testing.T) {
	mod := &packages.Module{Path: "example.com/app", Main: true}
	clean := &Loaded{All: []*packages.Package{{PkgPath: "example.com/app/clean", Module: mod}}}
	if err := clean.NonInjectorErrors(); err != nil {
		t.Errorf("NonInjectorErrors on a load without errors = %v, want nil", err)
	}
	untyped := &packages.Package{PkgPath: "example.com/app/untyped", Module: mod, Errors: []packages.Error{{Msg: "undefined: New"}}}
	l := &Loaded{All: []*packages.Package{untyped}}
	if err := l.NonInjectorErrors(); err == nil || !strings.Contains(err.Error(), "undefined: New") {
		t.Errorf("NonInjectorErrors = %v, want the untyped package's error reported", err)
	}
}

// TestAHandBuiltLoadAnswersAsALoadedOne: a Loaded built by hand — no memo,
// no selection, no main-module set, the shape tests and tools assemble —
// answers as the loaded one does. Its specs, an injector's candidates and
// the injectors hidden in other configurations come from scanning All
// directly, with Module.Main deciding what is the user's code.
func TestAHandBuiltLoadAnswersAsALoadedOne(t *testing.T) {
	const apiPkg, workerPkg = "example.com/variantinjectors/cmd/api", "example.com/variantinjectors/cmd/worker"
	l := mustLoad(t, Config{Dir: writeVariantInjectorModule(t), Build: BuildFlags{Tags: "prod"}})
	hand := &Loaded{Tags: l.Tags, Fset: l.Fset, All: l.All, ByPath: l.ByPath, ServoPkg: l.ServoPkg}

	loadedSpecs, err := ModuleSpecs(l)
	if err != nil {
		t.Fatalf("ModuleSpecs(loaded): %v", err)
	}
	handSpecs, err := ModuleSpecs(hand)
	if err != nil {
		t.Fatalf("ModuleSpecs(by hand): %v", err)
	}
	if len(handSpecs) != 1 || len(loadedSpecs) != 1 || handSpecs[0].InjectorPkg.PkgPath != apiPkg || handSpecs[0].GeneratedConstraint != loadedSpecs[0].GeneratedConstraint {
		t.Fatalf("specs by hand = %v, loaded = %v; want the one prod spec of %s, alike", handSpecs, loadedSpecs, apiPkg)
	}

	injector := l.ByPath[apiPkg]
	handAccepted, handRejected := hand.Candidates(injector)
	accepted, rejected := l.Candidates(injector)
	if got, want := describeProviders(handAccepted), describeProviders(accepted); !reflect.DeepEqual(got, want) {
		t.Errorf("candidates by hand:\n%v\nloaded:\n%v", got, want)
	}
	if got, want := describeRejected(handRejected), describeRejected(rejected); !reflect.DeepEqual(got, want) {
		t.Errorf("rejections by hand:\n%v\nloaded:\n%v", got, want)
	}

	if got := hand.InjectorsInOtherConfigurations(apiPkg); !reflect.DeepEqual(got, []string{workerPkg}) {
		t.Errorf("InjectorsInOtherConfigurations by hand = %v, want [%s]", got, workerPkg)
	}
}

// TestStdlibErrorIsOnlyAStandardLibraryTypeError picks out the one failure the
// from-source fallback can add: a type or parse error inside a
// standard-library package, from a go/types older than the library it
// reads. A module package's errors, and a standard-library list error such
// as an import of a std path that does not exist, stay where
// NonInjectorErrors reports them.
func TestStdlibErrorIsOnlyAStandardLibraryTypeError(t *testing.T) {
	module := &packages.Package{PkgPath: "example.com/app", Module: &packages.Module{Path: "example.com/app"}, Errors: []packages.Error{{Kind: packages.TypeError, Pos: "app.go:1:1", Msg: "undefined: x"}}}
	missing := &packages.Package{PkgPath: "nosuch/pkg", Errors: []packages.Error{{Kind: packages.ListError, Msg: "package nosuch/pkg is not in std"}}}
	if err := stdlibError([]*packages.Package{module, missing}); err != nil {
		t.Errorf("stdlibError = %v, want nil: neither is the standard library's own type error", err)
	}
	newer := &packages.Package{PkgPath: "net/http", Errors: []packages.Error{
		{Kind: packages.ListError, Msg: "# net/http"},
		{Kind: packages.TypeError, Pos: "server.go:9:2", Msg: "undefined: newer"},
	}}
	if err := stdlibError([]*packages.Package{module, missing, newer}); err == nil || err.Error() != "server.go:9:2: undefined: newer" {
		t.Errorf("stdlibError = %v, want net/http's type error", err)
	}
}

// TestLoadReportsAFailedBuildOfTheExportData covers the failures that stop
// the go command before it lists anything, and only when it has to build:
// the type-checked load asks it for export data, the earlier questions do
// not. go/packages reports no error for that call — it takes any failure
// for a package's own build error, which the packages carry — so the load
// came back empty, and servo said the runtime was not imported and to run
// `servo init`. The go command's own reason is what the user has to see.
func TestLoadReportsAFailedBuildOfTheExportData(t *testing.T) {
	dir := writeFixtureModule(t, "")
	type failure struct {
		name, env, value, want string
		overlay                bool
	}
	cases := []failure{
		{name: "the build cache turned off", env: "GOCACHE", value: "off", want: "build cache is disabled by GOCACHE=off"},
		// The go command is asked again with the overlay the run was
		// given; one it could not read would be the failure reported.
		{name: "the build cache turned off, under --overlay", env: "GOCACHE", value: "off", want: "build cache is disabled by GOCACHE=off", overlay: true},
	}
	if falseCmd, err := exec.LookPath("false"); err == nil {
		cases = append(cases, failure{name: "a -toolexec that fails", env: "GOFLAGS", value: "-toolexec=" + falseCmd, want: "error obtaining buildID for go tool compile"})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{Dir: dir}
			if c.overlay {
				api := filepath.Join(dir, "api", "api.go")
				src, err := os.ReadFile(api)
				if err != nil {
					t.Fatal(err)
				}
				replacements := t.TempDir()
				mustWriteFile(t, replacements, "api.go", string(src))
				cfg.Build.Overlay = writeOverlayFile(t, replacements, map[string]string{api: filepath.Join(replacements, "api.go")})
			}
			t.Setenv(c.env, c.value)
			_, err := Load(cfg)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load = %v, want the go command's %q", err, c.want)
			}
			for _, wrong := range []string{"servo init", "spec file"} {
				if strings.Contains(err.Error(), wrong) {
					t.Errorf("Load = %v, which still points at %s", err, wrong)
				}
			}
		})
	}
}
