package load

import (
	"errors"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// errDecode is the error x/tools' gcimporter gives for export data from
// a toolchain newer than itself — what Go 1.27.2 did to x/tools < v0.50.
var errDecode = errors.New(`internal error in importing "context" (cannot decode export data: export data version 5 is greater than maximum supported version 4); please report an issue`)

// failExportData makes every export-data read fail to decode, standing in
// for a go command whose format this binary's x/tools cannot read, and
// records which packages were attempted, in order.
func failExportData(t *testing.T) *[]string {
	t.Helper()
	var attempted []string
	saved := readExport
	readExport = func(p *packages.Package, _ *token.FileSet, _ map[string]*types.Package) error {
		attempted = append(attempted, p.PkgPath)
		return errDecode
	}
	t.Cleanup(func() { readExport = saved })
	return &attempted
}

// TestLoadFallsBackToSourceWhenExportDataCannotBeRead covers the failure
// mode the export-data loader brought with it: a Go release that moves the
// export format breaks every servo command until x/tools catches up. The
// standard library is type-checked from source instead — the loader's
// only mode before it read export data — so the run succeeds, slower, and
// says so once, with the remedy.
func TestLoadFallsBackToSourceWhenExportDataCannotBeRead(t *testing.T) {
	dir := writeFixtureModule(t, "")
	attempted := failExportData(t)

	l, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load = %v, want the from-source fallback to succeed", err)
	}
	if len(*attempted) == 0 {
		t.Fatal("fixture is not exercising anything: no export data was read")
	}
	for _, want := range []string{"slower", "go get golang.org/x/tools@latest", "install a newer one"} {
		if !strings.Contains(l.Note, want) {
			t.Errorf("Note = %q, want it to mention %q", l.Note, want)
		}
	}
	// The standard library is all there, from source: a candidate scan of
	// it, and identity across the boundary, are what export data was for.
	if ctx := l.ByPath["context"]; ctx == nil || ctx.Types == nil || !ctx.Types.Complete() || ctx.TypesInfo == nil {
		t.Errorf("context after the fallback = %+v, want it type-checked from source", ctx)
	}
	spec, err := FindSpec(l)
	if err != nil || len(spec.Binds) != 1 {
		t.Fatalf("FindSpec after the fallback = %v, %v; want the fixture's one Bind", spec, err)
	}
}

// TestAnUnopenableExportFileIsNotAFormatProblem keeps the fallback to the
// one failure it is for. A file that will not open is a build cache
// problem, which type-checking from source would only hide.
func TestAnUnopenableExportFileIsNotAFormatProblem(t *testing.T) {
	dir := writeFixtureModule(t, "")
	saved := readExport
	readExport = func(p *packages.Package, _ *token.FileSet, _ map[string]*types.Package) error {
		return &fs.PathError{Op: "open", Path: p.ExportFile, Err: fs.ErrNotExist}
	}
	t.Cleanup(func() { readExport = saved })

	_, err := Load(Config{Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "load: export data for") || strings.Contains(err.Error(), "cannot read the export data") {
		t.Fatalf("Load = %v, want the open failure reported as itself", err)
	}
}

// TestExportDataFailureIsReportedAgainstTheSamePackageEveryRun covers the
// map walk that named a different standard-library package on each run.
func TestExportDataFailureIsReportedAgainstTheSamePackageEveryRun(t *testing.T) {
	dir := writeFixtureModule(t, "")
	attempted := failExportData(t)
	if _, err := Load(Config{Dir: dir}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	first := (*attempted)[0]
	for i := 0; i < 2; i++ {
		*attempted = nil
		if _, err := Load(Config{Dir: dir}); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if (*attempted)[0] != first {
			t.Fatalf("the first export data read was %s, then %s", first, (*attempted)[0])
		}
	}
}

// TestExportDataErrorNamesBothVersionsAndBothRemedies pins the message
// for the case the fallback cannot rescue: which reader, which writer, the
// reader's own words, and both ways out — a newer x/tools in the module
// for `go tool servo`, whose build list is the module's, and a newer servo
// for an installed binary, which carries the x/tools its release pinned.
func TestExportDataErrorNamesBothVersionsAndBothRemedies(t *testing.T) {
	e := &exportDataError{pkg: "context", err: errDecode, tools: "v0.49.0", goVersion: "go1.27.2", fromSource: errors.New("net/http: undefined: newer")}
	msg := e.Error()
	for _, want := range []string{
		"golang.org/x/tools v0.49.0 cannot read the export data go1.27.2 wrote for context",
		"export data version 5 is greater than maximum supported version 4",
		"type-checking the standard library from source instead failed too: net/http: undefined: newer",
		"with `go tool servo`, run `go get golang.org/x/tools@latest` in your module",
		"with an installed servo, install a newer one",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q\nwant it to contain %q", msg, want)
		}
	}
	unknown := (&exportDataError{pkg: "context", err: errDecode}).Error()
	if !strings.Contains(unknown, "the golang.org/x/tools this servo is built with cannot read the export data this Go release wrote") {
		t.Errorf("with neither version known, Error() = %q", unknown)
	}
}

// TestToolsVersionIsReadFromTheBinary covers where the reader's version
// comes from: the build info of the running binary, which under `go tool
// servo` is the consumer's build list rather than servo's go.mod.
func TestToolsVersionIsReadFromTheBinary(t *testing.T) {
	tools := func(version string, replace *debug.Module) *debug.Module {
		return &debug.Module{Path: toolsModule, Version: version, Replace: replace}
	}
	for _, tc := range []struct {
		name string
		deps []*debug.Module
		want string
	}{
		{"required", []*debug.Module{{Path: "golang.org/x/mod", Version: "v0.42.0"}, tools("v0.52.0", nil)}, "v0.52.0"},
		{"replaced by another version", []*debug.Module{tools("v0.52.0", &debug.Module{Path: toolsModule, Version: "v0.53.0"})}, "v0.53.0"},
		{"replaced by a directory", []*debug.Module{tools("v0.52.0", &debug.Module{Path: "../tools"})}, ""},
		{"not linked at all", []*debug.Module{{Path: "golang.org/x/mod", Version: "v0.42.0"}}, ""},
	} {
		if got := toolsVersionFrom(&debug.BuildInfo{Deps: tc.deps}); got != tc.want {
			t.Errorf("%s: toolsVersionFrom = %q, want %q", tc.name, got, tc.want)
		}
	}

	// The running binary, when its build info records x/tools at all: Go
	// 1.27's test binaries do, Go 1.26's did not, and a release binary
	// always does.
	if info, ok := debug.ReadBuildInfo(); ok {
		if want := toolsVersionFrom(info); want != "" && toolsVersion() != want {
			t.Errorf("toolsVersion() = %q, want this test binary's %q", toolsVersion(), want)
		}
	}
	if v := goVersion("."); !strings.HasPrefix(v, "go1.") {
		t.Errorf("goVersion(.) = %q, want the go command's GOVERSION", v)
	}
	// Where the go command cannot even start, there is no release to name,
	// and the message says "this Go release" instead.
	if v := goVersion(filepath.Join(t.TempDir(), "missing")); v != "" {
		t.Errorf("goVersion(a missing directory) = %q, want \"\"", v)
	}
}
