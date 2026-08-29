package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

const scopedAppSrc = `
package app

import "context"

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }
func (l *Logger) Stop(ctx context.Context) error { return nil }

type RoomKey string

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Lobbies interface {
	Acquire(ctx context.Context) (*Lobby, func(), error)
}

// RoomLog is scoped transitively — it takes the key, nothing else — and
// carries a cleanup func so the entry's cleanup wiring is exercised too.
type RoomLog struct{}
func (l *RoomLog) Init(ctx context.Context) error  { return nil }
func (l *RoomLog) Flush(ctx context.Context) error { return nil }
func NewRoomLog(k RoomKey, l *Logger) (*RoomLog, func()) { return &RoomLog{}, func() {} }

type Room struct{}
func (r *Room) Init(ctx context.Context) error  { return nil }
func (r *Room) Run(ctx context.Context) error   { return nil }
func (r *Room) Drain(ctx context.Context) error { return nil }
func (r *Room) Stop(ctx context.Context) error  { return nil }
func (r *Room) Health(ctx context.Context) error { return nil }
func NewRoom(k RoomKey, rl *RoomLog) (*Room, error) { return &Room{}, nil }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

// Lobby is a second exposed root on the same key type, so one registry
// serves two accessors.
type Lobby struct{}
func (l *Lobby) Init(ctx context.Context) error { return nil }
func NewLobby(k RoomKey) *Lobby { return &Lobby{} }
func (_ *Lobby) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Server struct{}
func (s *Server) Run(ctx context.Context) error   { return nil }
func (s *Server) Drain(ctx context.Context) error { return nil }
func NewServer(r Rooms, lb Lobbies, l *Logger) *Server { return &Server{} }
`

func buildScopedResolved(t *testing.T) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", scopedAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	appPkg := &packages.Package{
		Name: "app", PkgPath: "example.com/app",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }

	spec := &load.Spec{
		InjectorPkg: appPkg,
		Roots: []load.RootDecl{
			{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 9}},
		},
		Scopes: []load.ScopeDecl{
			{
				Impl: graph.NewKey(ptr("Room"), ""), ImplType: ptr("Room"),
				Iface: graph.NewKey(lookup("Rooms"), ""), IfaceType: lookup("Rooms"),
				Linger: 90 * time.Second, LingerSet: true,
				Max: 250, MaxSet: true,
				Pos: token.Position{Filename: "spec.go", Line: 10},
			},
			{
				Impl: graph.NewKey(ptr("Lobby"), ""), ImplType: ptr("Lobby"),
				Iface: graph.NewKey(lookup("Lobbies"), ""), IfaceType: lookup("Lobbies"),
				Pos: token.Position{Filename: "spec.go", Line: 14},
			},
		},
	}

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/app": true},
		Fset:       fset,
		Pkgs:       []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

func TestEmitScopedApp(t *testing.T) {
	resolved, spec := buildScopedResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	for _, want := range []string{
		// One registry, one entry type, two accessors.
		"type roomKeyScope struct {",
		"type roomKeyEntry struct {",
		"type roomsAccessor struct{ s *roomKeyScope }",
		"type lobbiesAccessor struct{ s *roomKeyScope }",
		"func (x roomsAccessor) Acquire(ctx context.Context) (*Room, func(), error)",
		"func (x lobbiesAccessor) Acquire(ctx context.Context) (*Lobby, func(), error)",
		"func (x roomsAccessor) Stats() servo.ScopeStats",

		// Policy, read once through servo.LingerWindow so servotest can
		// shorten it.
		"servo.LingerWindow(90 * time.Second)",
		"max:     250,",

		// The extractor is called on a typed nil.
		"var zero *Room",
		"key, err := zero.ScopeKey(ctx)",
		"return nil, nil, servo.ErrNoLifetime",

		// The race-critical ordering: leave the map, then announce.
		"e.scope.beginTeardown(e)\n\tclose(e.dead)",
		// An entry mid-teardown has left items but is still an instance:
		// Shutdown must wait for it and Max must count it.
		"func (s *roomKeyScope) liveLocked() int { return len(s.items) + len(s.tearing) }",
		"if s.liveLocked() >= s.max {",
		"for e := range s.tearing {",
		// Every wait during shutdown is budgeted — and by as many budgets
		// as the teardown it waits for actually spends.
		"servo.RunStop(ctx, 6*servo.DefaultStopBudget, e.name(), e.waitTorn)",
		// A panic anywhere in construction unwinds through rollback.
		"func (e *roomKeyEntry) rollback() {",
		"entry.rollback()",
		// An Init panic on a concurrent level must not escape the errgroup.
		"if r := recover(); r != nil {\n\t\t\t\t\terr = fmt.Errorf(\"servo: panic in Init of",
		// The loop stops accepting joins once Shutdown has begun, and
		// waits for the references it already handed out before tearing
		// the instance down under them.
		"select {\n\t\tcase <-e.scope.quit:\n\t\t\tstopTimer()\n\t\t\te.drainRefs(refs)\n\t\t\te.evict()\n\t\t\treturn\n\t\tdefault:\n\t\t}",
		"func (e *roomKeyEntry) drainRefs(refs int) {",
		// A would-be joiner is told the scope is closed rather than
		// waiting out the drain it is not part of.
		"case <-s.quit:\n\t\t\t// Shutdown began while this acquirer was waiting to join.",
		// A panic in a user constructor must not orphan the entry.
		"func (s *roomKeyScope) start(entry *roomKeyEntry) (err error) {",
		"s.abandon(entry, fmt.Errorf(\"servo: panic constructing %s: %v\", entry.name(), r))",
		// A freshly built entry that Shutdown already tore down is not
		// handed back with a nil error.
		"select {\n\t\t\tcase <-e.dead:\n\t\t\t\tcontinue\n\t\t\tdefault:\n\t\t\t}",

		// Teardown: drain, cancel, wait for Run, then flush/stop/cleanup.
		"results = append(results, e.drainRoom(ctx)...)",
		"e.cancel()",
		"e.waitRun)",
		"e.roomLog.Flush",
		"e.roomLogCleanup()",

		// The scope is stopped between its consumer and the logger it
		// borrows.
		"nodes = append(nodes, a.stopServer(ctx))\n\tnodes = append(nodes, a.stopRoomKeyScope(ctx))\n\tnodes = append(nodes, a.stopLogger(ctx))",

		// Scope attribution in the static graph.
		`Scope: "example.com/app.RoomKey"`,
		`Scopes: []servo.GraphScope{`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}

	// Health and Ready are deliberately not wired for scoped nodes: a
	// report with one entry per live room is not a report.
	if strings.Contains(src, "e.room.Health") || strings.Contains(src, "a.room.Health") {
		t.Error("a scoped node's Health must not be wired")
	}
	// Lobby depends on nothing but the key, so it is still a member: it
	// is built inside the entry, never assigned to an App field.
	if !strings.Contains(src, "lobby := NewLobby(e.key)") {
		t.Error("Lobby was not constructed per entry")
	}
	if strings.Contains(src, "a.lobby = lobby") {
		t.Error("Lobby was constructed in New as an app singleton")
	}
}

// TestEmitScopedTestApp covers the override variant: both files land in
// the same package, so every package-level scope declaration has to be
// distinct from the production file's.
func TestEmitScopedTestApp(t *testing.T) {
	resolved, spec := buildScopedResolved(t)
	prod, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	test, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit(testMode): %v", err)
	}

	for _, want := range []string{
		"type testRoomKeyScope struct {",
		"type testRoomKeyEntry struct {",
		"type testRoomsAccessor struct{ s *testRoomKeyScope }",
		"func newTestRoomKeyScope(ctx context.Context, app *TestApp) *testRoomKeyScope",
	} {
		if !strings.Contains(string(test), want) {
			t.Errorf("override variant missing %q", want)
		}
	}
	for _, mustNotShare := range []string{
		"type roomKeyScope struct {",
		"type roomsAccessor struct{",
		"func newRoomKeyScope(",
	} {
		if strings.Contains(string(test), mustNotShare) {
			t.Errorf("override variant redeclares %q, which the production file already declares", mustNotShare)
		}
	}
	if !strings.Contains(string(prod), "type roomKeyScope struct {") {
		t.Error("production variant lost its own scope type")
	}
}

func TestEmitScopedGolden(t *testing.T) {
	resolved, spec := buildScopedResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden", "scopedapp.go.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("golden file updated")
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(out) != string(want) {
		t.Errorf("output does not match golden file %s (run with UPDATE_GOLDEN=1 to review+accept changes)\n--- got ---\n%s", goldenPath, out)
	}
}

// TestEmitIsDeterministic guards the one property every golden file
// depends on: two emissions of the same plan are byte-identical, whatever
// order the maps inside the emitter happened to iterate in.
func TestEmitScopedIsDeterministic(t *testing.T) {
	resolved, spec := buildScopedResolved(t)
	first, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for i := range 5 {
		again, err := Emit(resolved, spec, false)
		if err != nil {
			t.Fatalf("Emit #%d: %v", i, err)
		}
		if string(again) != string(first) {
			t.Fatalf("emission #%d differs from the first", i)
		}
	}
}

func TestDurationLiteral(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0"},
		{time.Hour, "time.Hour"},
		{90 * time.Second, "90 * time.Second"},
		{time.Minute, "time.Minute"},
		{1500 * time.Millisecond, "1500 * time.Millisecond"},
		{time.Microsecond, "time.Microsecond"},
		{7 * time.Nanosecond, "7 * time.Nanosecond"},
		{2 * time.Hour, "2 * time.Hour"},
	} {
		if got := durationLiteral(tc.d); got != tc.want {
			t.Errorf("durationLiteral(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// crossAccessorSrc is the documented way out of the cross-scope
// rejection: a member of one scope holds another scope's *accessor* and
// acquires from it per call, rather than holding an instance of it.
const crossAccessorSrc = `
package app

import "context"

type TenantKey string
type RoomKey string

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}
type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Tenant struct{}
func NewTenant(k TenantKey) *Tenant { return &Tenant{} }
func (_ *Tenant) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }

type Room struct{}
func NewRoom(k RoomKey, t Tenants) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Server struct{}
func NewServer(r Rooms) *Server { return &Server{} }
`

// TestEmitScopeHoldingAnotherScopesAccessor covers the one edge that
// crosses two scopes legally, and the shutdown ordering it implies: the
// room scope has to stop before the tenant scope it acquires from.
func TestEmitScopeHoldingAnotherScopesAccessor(t *testing.T) {
	resolved, spec := buildResolvedFrom(t, crossAccessorSrc, []string{"Server"}, [][2]string{
		{"Room", "Rooms"}, {"Tenant", "Tenants"},
	})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)

	// The room's constructor takes the tenant accessor off the App, since
	// an accessor is not built by any provider.
	if !strings.Contains(src, "room := NewRoom(e.key, a.tenants)") {
		t.Errorf("the room entry does not take the tenant accessor:\n%s", src)
	}
	// Two registries, not one: the key types differ.
	for _, want := range []string{"type roomKeyScope struct {", "type tenantKeyScope struct {"} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q", want)
		}
	}
	// Shutdown sequences them: the room scope acquires from the tenant
	// scope, so it must stop first.
	roomAt := strings.Index(src, "nodes = append(nodes, a.stopRoomKeyScope(ctx))")
	tenantAt := strings.Index(src, "nodes = append(nodes, a.stopTenantKeyScope(ctx))")
	if roomAt < 0 || tenantAt < 0 {
		t.Fatalf("both scope stops must appear in Shutdown:\n%s", src)
	}
	if roomAt > tenantAt {
		t.Error("the tenant scope stops before the room scope that acquires from it")
	}
}

// buildResolvedFrom is buildScopedResolved generalized over the fixture,
// so a second shape does not need a second copy of the plumbing.
func buildResolvedFrom(t *testing.T, src string, rootNames []string, scopes [][2]string) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	appPkg := &packages.Package{
		Name: "app", PkgPath: "example.com/app",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }

	spec := &load.Spec{InjectorPkg: appPkg}
	for i, name := range rootNames {
		spec.Roots = append(spec.Roots, load.RootDecl{
			Key: graph.NewKey(ptr(name), ""), Type: ptr(name),
			Pos: token.Position{Filename: "spec.go", Line: 9 + i},
		})
	}
	for i, pair := range scopes {
		spec.Scopes = append(spec.Scopes, load.ScopeDecl{
			Impl: graph.NewKey(ptr(pair[0]), ""), ImplType: ptr(pair[0]),
			Iface: graph.NewKey(lookup(pair[1]), ""), IfaceType: lookup(pair[1]),
			Pos: token.Position{Filename: "spec.go", Line: 20 + i},
		})
	}

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       caps,
		Scope:      map[string]bool{"example.com/app": true},
		Fset:       fset,
		Pkgs:       []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}
