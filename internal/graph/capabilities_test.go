package graph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/packages"
)

// The servo package imports net/http, so loading it type-checks that whole
// tree, and the capability and ScopeKey fixtures each did it — about
// fifteen loads per run, most of this package's test time under -race. Load
// it once for the binary, as internal/route, internal/emit,
// internal/resolve and servovet already do; the result is read-only.
var (
	servoPkgOnce sync.Once
	servoPkgVal  *packages.Package
	servoPkgErr  string
)

func loadServoPackage(t *testing.T) *packages.Package {
	t.Helper()
	servoPkgOnce.Do(func() {
		cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
		pkgs, err := packages.Load(cfg, ServoPackagePath)
		switch {
		case err != nil:
			servoPkgErr = err.Error()
		case len(pkgs) != 1 || pkgs[0].Types == nil:
			servoPkgErr = fmt.Sprintf("expected exactly one loaded servo package, got %d", len(pkgs))
		case len(pkgs[0].Errors) > 0:
			servoPkgErr = pkgs[0].Errors[0].Error()
		default:
			servoPkgVal = pkgs[0]
		}
	})
	if servoPkgErr != "" {
		t.Fatalf("load servo package: %s", servoPkgErr)
	}
	return servoPkgVal
}

// pkgImporter resolves imports to the exact *types.Package objects a prior
// go/packages load produced, so a second, independent types.Config.Check
// (for a fixture that also imports "context") shares object identity with
// the first session. Without this, types.Implements structurally fails on
// every method with a context.Context parameter: the two sessions would
// otherwise produce two non-identical Named types both printing as
// "context.Context".
type pkgImporter struct{ byPath map[string]*types.Package }

func newPkgImporter(roots ...*packages.Package) *pkgImporter {
	idx := &pkgImporter{byPath: map[string]*types.Package{}}
	var add func(p *packages.Package)
	add = func(p *packages.Package) {
		if _, ok := idx.byPath[p.PkgPath]; ok {
			return
		}
		idx.byPath[p.PkgPath] = p.Types
		for _, dep := range p.Imports {
			add(dep)
		}
	}
	for _, p := range roots {
		add(p)
	}
	return idx
}

func (i *pkgImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if pkg, ok := i.byPath[path]; ok {
		return pkg, nil
	}
	return nil, fmt.Errorf("pkgImporter: package %q not found", path)
}

const capsFixtureSrc = `
package fixture

import "context"

type Full struct{}
func (*Full) Init(ctx context.Context) error   { return nil }
func (*Full) Run(ctx context.Context) error    { return nil }
func (*Full) Drain(ctx context.Context) error  { return nil }
func (*Full) Flush(ctx context.Context) error  { return nil }
func (*Full) Stop(ctx context.Context) error   { return nil }
func (*Full) Health(ctx context.Context) error { return nil }
func (*Full) Ready(ctx context.Context) error  { return nil }

type Plain struct{}

type OnlyRunner struct{}
func (OnlyRunner) Run(ctx context.Context) error { return nil }

// FullWrapper embeds *Full by pointer: Go promotes an embedded pointer's
// methods to the outer type's own value method set, so FullWrapper (not
// *FullWrapper) should structurally satisfy every capability Full does,
// without declaring any method itself.
type FullWrapper struct{ *Full }

// RunnerWrapper embeds OnlyRunner by value, whose Run has a value
// receiver: promotion should work the same way for a value embed.
type RunnerWrapper struct{ OnlyRunner }
`

func TestCapabilitiesDetect(t *testing.T) {
	servoPkg := loadServoPackage(t)
	caps, err := LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", capsFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	fixturePkg, err := conf.Check("example.com/fixture", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	typeNamed := func(name string) types.Type {
		obj := fixturePkg.Scope().Lookup(name)
		if obj == nil {
			t.Fatalf("no type %s in fixture", name)
		}
		return types.NewPointer(obj.Type())
	}

	full := caps.Detect(typeNamed("Full"))
	if len(full) != len(AllCapabilities) {
		t.Errorf("Full: got capabilities %v, want all of %v", full, AllCapabilities)
	}

	plain := caps.Detect(typeNamed("Plain"))
	if len(plain) != 0 {
		t.Errorf("Plain: got capabilities %v, want none", plain)
	}

	// OnlyRunner declares a value-receiver Run, so both *OnlyRunner and
	// OnlyRunner satisfy Runner.
	onlyRunnerObj := fixturePkg.Scope().Lookup("OnlyRunner").Type()
	got := caps.Detect(onlyRunnerObj)
	if len(got) != 1 || got[0] != "Runner" {
		t.Errorf("OnlyRunner: got %v, want [Runner]", got)
	}
}

// TestCapabilitiesDetectThroughEmbedding covers a struct that never
// declares a capability method itself, only inherits one via an embedded
// field — the "wrap in a distinct type" pattern the README recommends for
// disambiguating multiple instances of the same underlying type relies on
// promoted methods still being detected structurally.
func TestCapabilitiesDetectThroughEmbedding(t *testing.T) {
	servoPkg := loadServoPackage(t)
	caps, err := LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", capsFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	fixturePkg, err := conf.Check("example.com/fixture", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	// FullWrapper embeds *Full: promotion should carry all 7 capabilities
	// through to FullWrapper's own (bare, non-pointer) method set.
	fullWrapper := fixturePkg.Scope().Lookup("FullWrapper").Type()
	got := caps.Detect(fullWrapper)
	if len(got) != len(AllCapabilities) {
		t.Errorf("FullWrapper: got capabilities %v, want all of %v", got, AllCapabilities)
	}

	// RunnerWrapper embeds OnlyRunner by value: promotion should carry
	// just Runner through, the same as OnlyRunner itself.
	runnerWrapper := fixturePkg.Scope().Lookup("RunnerWrapper").Type()
	got = caps.Detect(runnerWrapper)
	if len(got) != 1 || got[0] != "Runner" {
		t.Errorf("RunnerWrapper: got %v, want [Runner]", got)
	}
}

func TestEmptyCapabilitiesDetectsNothing(t *testing.T) {
	servoPkg := loadServoPackage(t)
	caps := EmptyCapabilities()
	got := caps.Detect(types.NewPointer(servoPkg.Types.Scope().Lookup("Marker").Type()))
	if len(got) != 0 {
		t.Errorf("EmptyCapabilities: got %v, want none", got)
	}
}

func checkFixturePkg(t *testing.T, importPath, src string) *types.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: nil}
	pkg, err := conf.Check(importPath, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	return pkg
}

// A runtime missing a capability is the skew between a module's pinned
// servo release and a newer CLI, not a broken module: the capabilities it
// does declare are still detected, the rest are skipped, and Warning names
// them instead of every command failing.
func TestLoadCapabilitiesSkipsWhatAnOlderRuntimeLacks(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/older", `
package older

// An older runtime's shapes; the fixture spells them without a context so
// it type-checks with no importer.
type Initializer interface{ Init() error }
type Runner interface{ Run() error }
type Drainer interface{ Drain() error }
type Flusher interface{ Flush() error }
type Finalizer interface{ Stop() error }
type Healther interface{ Health() error }

type Full struct{}
func (*Full) Init() error   { return nil }
func (*Full) Health() error { return nil }
func (*Full) Ready() error  { return nil }
`)
	caps, err := LoadCapabilities(pkg)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v — a missing capability must be skipped, not fatal", err)
	}
	if got := caps.Missing(); len(got) != 1 || got[0] != "Readier" {
		t.Fatalf("Missing() = %v, want [Readier]", got)
	}
	got := caps.Detect(types.NewPointer(pkg.Scope().Lookup("Full").Type()))
	if strings.Join(got, ",") != "Initializer,Healther" {
		t.Errorf("Detect = %v, want [Initializer Healther]: Ready goes undetected, the rest still work", got)
	}
	w := caps.Warning("v3.6.0", "v3.5.0")
	for _, want := range []string{"example.com/older v3.5.0 declares no Readier", "servo v3.6.0 detects", "older servo than the CLI", "go tool servo"} {
		if !strings.Contains(w, want) {
			t.Errorf("Warning() = %q, want it to contain %q", w, want)
		}
	}
	if strings.Contains(w, "\n") {
		t.Errorf("Warning() spans several lines, want one: %q", w)
	}
	// Versions the caller cannot name are left out rather than printed
	// empty.
	if w := caps.Warning("", ""); !strings.Contains(w, "example.com/older declares no Readier, a capability this servo CLI detects") {
		t.Errorf("Warning without versions = %q", w)
	}
}

func TestLoadCapabilitiesWarnsAboutEveryMissingOne(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/incomplete", `
package incomplete

type Initializer interface{ Init() }
`)
	caps, err := LoadCapabilities(pkg)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}
	if got := len(caps.Missing()); got != len(AllCapabilities)-1 {
		t.Fatalf("Missing() has %d names, want %d", got, len(AllCapabilities)-1)
	}
	if w := caps.Warning("", ""); !strings.Contains(w, "declares none of Runner, Drainer, Flusher, Finalizer, Healther, Readier") {
		t.Errorf("Warning() = %q, want every missing capability named", w)
	}
}

// A runtime declaring none of the seven did not load — every one has
// existed since the first generator release — and a warning followed by a
// graph with no lifecycle would only hide that.
func TestLoadCapabilitiesRefusesARuntimeWithNone(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/empty", "package empty\n")
	_, err := LoadCapabilities(pkg)
	if err == nil || !strings.Contains(err.Error(), "declares none of the capability interfaces") || !strings.Contains(err.Error(), "go mod tidy") {
		t.Fatalf("got err=%v, want the did-not-load refusal", err)
	}
}

func TestCapabilitiesWarningEmptyWhenRuntimeMatches(t *testing.T) {
	caps, err := LoadCapabilities(loadServoPackage(t).Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}
	if len(caps.Missing()) != 0 || caps.Warning("v1", "v2") != "" {
		t.Errorf("Missing() = %v, Warning() = %q, want neither for the runtime this CLI was built with", caps.Missing(), caps.Warning("v1", "v2"))
	}
}

// TestCapabilityNearMisses covers the spellings that used to leave a
// component at "capabilities: none" with nothing said: Close-style names
// are not near misses (UnstoppedCloser covers them), but a capability's own
// name with the wrong signature, or on a receiver the node's method set
// leaves out, is.
func TestCapabilityNearMisses(t *testing.T) {
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "nearmiss.go", `
package nearmiss

import "context"

type Pool struct{}
func (*Pool) Close() error { return nil }

type Consumer struct{}
func (*Consumer) Run() error { return nil }

type Cache struct{}
func (*Cache) Stop(ctx context.Context) {}

type Server struct{}
func (*Server) Shutdown(ctx context.Context) error { return nil }
func (*Server) Init() error                        { return nil }

type Value struct{}
func (*Value) Health(ctx context.Context) error { return nil }

type Fine struct{}
func (*Fine) Run(ctx context.Context) error  { return nil }
func (*Fine) Stop(ctx context.Context) error { return nil }
func (*Fine) Close() error                   { return nil }
`, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg, err := (&types.Config{Importer: newPkgImporter(servoPkg)}).Check("example.com/nearmiss", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	ptr := func(name string) types.Type { return types.NewPointer(pkg.Scope().Lookup(name).Type()) }
	misses := func(t types.Type) []string {
		var out []string
		for _, m := range CapabilityNearMisses(t) {
			out = append(out, m.Capability+": "+m.Method.Name()+" "+m.Problem)
		}
		return out
	}

	for name, want := range map[string]string{
		"Consumer": "Runner: Run has the signature Run() error, not Run(ctx context.Context) error",
		"Cache":    "Finalizer: Stop has the signature Stop(ctx context.Context), not Stop(ctx context.Context) error",
		"Server":   "Initializer: Init has the signature Init() error, not Init(ctx context.Context) error",
	} {
		if got := misses(ptr(name)); len(got) != 1 || got[0] != want {
			t.Errorf("%s: near misses = %q, want [%q]", name, got, want)
		}
	}
	for _, name := range []string{"Pool", "Fine"} {
		if got := misses(ptr(name)); len(got) != 0 {
			t.Errorf("%s: near misses = %q, want none", name, got)
		}
	}
	// A constructor returning the value type hides every pointer-receiver
	// method from the node's method set.
	if got := misses(pkg.Scope().Lookup("Value").Type()); len(got) != 1 || !strings.Contains(got[0], "Healther: Health is declared on the pointer receiver, but the node is the value example.com/nearmiss.Value") {
		t.Errorf("Value: near misses = %q, want the pointer-receiver one", got)
	}

	for name, want := range map[string]string{"Pool": "Close", "Server": "Shutdown"} {
		if fn := UnstoppedCloser(ptr(name)); fn == nil || fn.Name() != want {
			t.Errorf("UnstoppedCloser(%s) = %v, want its %s", name, fn, want)
		}
	}
	for _, name := range []string{"Fine", "Consumer"} {
		if fn := UnstoppedCloser(ptr(name)); fn != nil {
			t.Errorf("UnstoppedCloser(%s) = %v, want nil — it has a Stop, or nothing to close", name, fn)
		}
	}
}

func TestLoadCapabilitiesNotAnInterface(t *testing.T) {
	pkg := checkFixturePkg(t, "example.com/wrongkind", `
package wrongkind

type Initializer struct{}
type Runner interface{}
type Drainer interface{}
type Flusher interface{}
type Finalizer interface{}
type Healther interface{}
type Readier interface{}
`)
	_, err := LoadCapabilities(pkg)
	if err == nil || !strings.Contains(err.Error(), "is not an interface") {
		t.Fatalf("got err=%v, want an 'is not an interface' error", err)
	}
}
