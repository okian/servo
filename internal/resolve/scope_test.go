package resolve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

const scopeAppSrc = `
package scoped

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type RoomKey string
type TenantKey string

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}

// RoomLog is scoped only transitively: it takes the key, nothing more.
type RoomLog struct{}
func NewRoomLog(k RoomKey, l *Logger) *RoomLog { return &RoomLog{} }

type Room struct{}
func NewRoom(k RoomKey, rl *RoomLog) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Tenant struct{}
func NewTenant(k TenantKey) *Tenant { return &Tenant{} }
func (_ *Tenant) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }

// Nested is keyed by RoomKey but reaches into the TenantKey scope.
type Nested struct{}
func NewNested(k RoomKey, t *Tenant) *Nested { return &Nested{} }
func NewTenantHolder(w *Widget, k TenantKey) *TenantHolder { return &TenantHolder{} }
type TenantHolder struct{}
func (_ *TenantHolder) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type TenantHolders interface {
	Acquire(ctx context.Context) (*TenantHolder, func(), error)
}
func (_ *Nested) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type Nesteds interface {
	Acquire(ctx context.Context) (*Nested, func(), error)
}

// Lobby shares RoomKey with Room: two Scoped declarations whose
// extractors return the same defined type are one scope.
type Lobby struct{}
func NewLobby(k RoomKey) *Lobby { return &Lobby{} }
func (_ *Lobby) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type Lobbies interface {
	Acquire(ctx context.Context) (*Lobby, func(), error)
}

// Server is the well-behaved consumer: it depends on the accessor.
type Server struct{}
func NewServer(r Rooms, l *Logger) *Server { return &Server{} }

// LogHolder widens a scoped node that no accessor exposes.
type LogHolder struct{}
func NewLogHolder(rl *RoomLog) *LogHolder { return &LogHolder{} }

// Widener is the mistake: a singleton holding the scoped type itself.
type Widener struct{}
func NewWidener(r *Room) *Widener { return &Widener{} }

// Undeclared has a ScopeKey but no servo.Scoped names it.
type Undeclared struct{}
func NewUndeclared(k TenantKey) *Undeclared { return &Undeclared{} }
func (_ *Undeclared) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type UndeclaredHolder struct{}
func NewUndeclaredHolder(u *Undeclared) *UndeclaredHolder { return &UndeclaredHolder{} }

// Declared is the same, except its accessor interface is already written.
// Only the servo.Scoped line is missing.
type Declared struct{}
func NewDeclared(k TenantKey) *Declared { return &Declared{} }
func (_ *Declared) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type Declareds interface {
	Acquire(ctx context.Context) (*Declared, func(), error)
}
type DeclaredHolder struct{}
func NewDeclaredHolder(d *Declared) *DeclaredHolder { return &DeclaredHolder{} }

// Blob's accessor is not Blob+"s", so a message that re-derives the name
// instead of using the one it found names a type that does not exist.
type Blob struct{}
func NewBlob(k TenantKey) *Blob { return &Blob{} }
func (_ *Blob) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type BlobPool interface {
	Acquire(ctx context.Context) (*Blob, func(), error)
}
type BlobHolder struct{}
func NewBlobHolder(b *Blob) *BlobHolder { return &BlobHolder{} }

// NoScopeKey is declared as scoped but has no extractor.
type NoScopeKey struct{}
func NewNoScopeKey() *NoScopeKey { return &NoScopeKey{} }
type NoScopeKeys interface {
	Acquire(ctx context.Context) (*NoScopeKey, func(), error)
}

// Extractor's key extractor depends on something that is itself scoped.
type Decoder struct{}
func NewDecoder(k RoomKey) *Decoder { return &Decoder{} }
type Extractor struct{}
func NewExtractor(k RoomKey) *Extractor { return &Extractor{} }
func (_ *Extractor) ScopeKey(ctx context.Context, d *Decoder) (RoomKey, error) { return "", nil }
type Extractors interface {
	Acquire(ctx context.Context) (*Extractor, func(), error)
}

// BadKey returns a bare string, which cannot identify a scope.
type BadKey struct{}
func NewBadKey() *BadKey { return &BadKey{} }
func (_ *BadKey) ScopeKey(ctx context.Context) (string, error) { return "", nil }
type BadKeys interface {
	Acquire(ctx context.Context) (*BadKey, func(), error)
}

// NamedRecv's extractor can reach its own receiver, which generated code
// calls on a typed nil.
type NamedRecv struct{ id RoomKey }
func NewNamedRecv(k RoomKey) *NamedRecv { return &NamedRecv{id: k} }
func (r *NamedRecv) ScopeKey(ctx context.Context) (RoomKey, error) { return r.id, nil }
type NamedRecvs interface {
	Acquire(ctx context.Context) (*NamedRecv, func(), error)
}

// Widget is the membership hole: it takes RoomKey but is reachable only
// from the TenantKey scope, so a fixpoint over each scope's own reach
// classifies it as a member of neither.
type Widget struct{}
func NewWidget(k RoomKey) *Widget { return &Widget{} }

// StolenRoot is a root of the TenantKey scope whose constructor also takes
// RoomKey — so both scopes claim it, and whichever is processed first wins
// silently.
type StolenRoot struct{}
func NewStolenRoot(rk RoomKey, tk TenantKey) *StolenRoot { return &StolenRoot{} }
func (_ *StolenRoot) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type StolenRoots interface {
	Acquire(ctx context.Context) (*StolenRoot, func(), error)
}

// RoomBridge pulls StolenRoot into the RoomKey scope's reach as well, so
// both scopes genuinely claim it.
type RoomBridge struct{}
func NewRoomBridge(k RoomKey, sr *StolenRoot) *RoomBridge { return &RoomBridge{} }
func (_ *RoomBridge) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type RoomBridges interface {
	Acquire(ctx context.Context) (*RoomBridge, func(), error)
}

// ValueScoped is scoped but not a pointer, which the emitter cannot
// express: Acquire has no zero to return alongside an error.
type ValueScoped struct{}
func NewValueScoped(k RoomKey) ValueScoped { return ValueScoped{} }
func (_ ValueScoped) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type ValueScopeds interface {
	Acquire(ctx context.Context) (ValueScoped, func(), error)
}

// PlainScopeKey has a method of that name with none of the extractor's
// shape — an ordinary type in an ordinary module that must not fail
// generation.
type PlainScopeKey struct{}
func NewPlainScopeKey() *PlainScopeKey { return &PlainScopeKey{} }
func (p *PlainScopeKey) ScopeKey() string { return "" }
type PlainHolder struct{}
func NewPlainHolder(p *PlainScopeKey) *PlainHolder { return &PlainHolder{} }

// AccessorExtractor's extractor takes another scope's accessor — an App
// field, set before any constructor runs, and the documented way out of a
// cross-scope dependency.
type AccessorExtractor struct{}
func NewAccessorExtractor(k TenantKey) *AccessorExtractor { return &AccessorExtractor{} }
func (_ *AccessorExtractor) ScopeKey(ctx context.Context, r Rooms) (TenantKey, error) { return "", nil }
type AccessorExtractors interface {
	Acquire(ctx context.Context) (*AccessorExtractor, func(), error)
}

// SelfAcquirer's extractor takes its OWN scope's accessor, which is
// unbounded recursion rather than a way out of anything.
type SelfAcquirer struct{}
func NewSelfAcquirer(k TenantKey) *SelfAcquirer { return &SelfAcquirer{} }
func (_ *SelfAcquirer) ScopeKey(ctx context.Context, s SelfAcquirers) (TenantKey, error) { return "", nil }
type SelfAcquirers interface {
	Acquire(ctx context.Context) (*SelfAcquirer, func(), error)
}

// NoErrorResult is the most dangerous extractor shape: one that forgot
// its error, so a missing key becomes the zero key and every keyless
// caller silently shares one instance.
type NoErrorResult struct{}
func NewNoErrorResult(k RoomKey) *NoErrorResult { return &NoErrorResult{} }
func (_ *NoErrorResult) ScopeKey(ctx context.Context) RoomKey { return "" }
type NoErrorHolder struct{}
func NewNoErrorHolder(n *NoErrorResult) *NoErrorHolder { return &NoErrorHolder{} }

// DirectRooms is a second, otherwise-identical accessor interface with an
// ordinary constructor returning it — an accepted candidate that
// resolution would never select, kept on its own interface so it only
// affects the one test that declares it.
type DirectRooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}
func NewRoomsDirectly() DirectRooms { return nil }

// GenericBox is an instantiated generic scoped type, so the suggested
// accessor has to carry its type arguments to be pastable.
type GenericBox[T any] struct{}
func NewGenericBox[T any](k RoomKey) *GenericBox[T] { return &GenericBox[T]{} }
func NewStringBox(k RoomKey) *GenericBox[string] { return &GenericBox[string]{} }
func (_ *GenericBox[T]) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type BoxHolder struct{}
func NewBoxHolder(b *GenericBox[string]) *BoxHolder { return &BoxHolder{} }

// PromotedKey reaches a ScopeKey only through an embedded field, which
// servo will not use — the diagnostic has to say so rather than claim the
// method is missing.
type KeyBase struct{}
func (_ *KeyBase) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type PromotedKey struct{ *KeyBase }
func NewPromotedKey() *PromotedKey { return &PromotedKey{} }
type PromotedKeys interface {
	Acquire(ctx context.Context) (*PromotedKey, func(), error)
}

// RoomsWithStats declares both accessor methods, which is the shape the
// examples use and the one that has to be accepted.
type RoomsWithStats interface {
	Acquire(ctx context.Context) (*Room, func(), error)
	Stats() servo.ScopeStats
}

// StrayAcross is keyed by TenantKey and takes RoomKey — a member of one
// scope holding another scope's key, without either scope claiming it
// twice.
type StrayAcross struct{}
func NewStrayAcross(tk TenantKey, rk RoomKey) *StrayAcross { return &StrayAcross{} }
func (*StrayAcross) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }
type StrayAcrosses interface {
	Acquire(ctx context.Context) (*StrayAcross, func(), error)
}

// StatsNotScopeStats returns something other than servo.ScopeStats.
type StatsNotScopeStats interface {
	Stats() RoomKey
}

// KeyEater is a singleton asking for a scope key directly.
type KeyEater struct{}
func NewKeyEater(k RoomKey) *KeyEater { return &KeyEater{} }

// Interfaces that the generated accessor cannot satisfy.
type BadMethod interface {
	Acquire(ctx context.Context) (*Room, func(), error)
	Reset()
}
type BadAcquire interface {
	Acquire(ctx context.Context) *Room
}
type BadStats interface {
	Stats() int
}
type BadAcquireParams interface {
	Acquire(ctx context.Context, k RoomKey) (*Room, func(), error)
}
type BadAcquireType interface {
	Acquire(ctx context.Context) (*RoomLog, func(), error)
}
type BadAcquireCleanup interface {
	Acquire(ctx context.Context) (*Room, int, error)
}
type BadAcquireError interface {
	Acquire(ctx context.Context) (*Room, func(), bool)
}
type BadStatsParams interface {
	Stats(n int) int
}
`

func checkScopeFixture(t *testing.T) (*types.Package, *token.FileSet, []*packages.Package, []*graph.Provider) {
	t.Helper()
	// Both, and by real load: the fixture's accessor interfaces name
	// servo.ScopeStats, and types.Implements only works when the two
	// type-checking sessions share object identity for it.
	ctxPkg := loadPkg(t, "context")
	servoPkg := loadPkg(t, graph.ServoPackagePath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scoped.go", scopeAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importerFor(ctxPkg, servoPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/scoped", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "scoped", PkgPath: "example.com/scoped",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}}
	accepted, _ := graph.ScanCandidates(pkgs, "example.com/scoped")
	return pkg, fset, pkgs, accepted
}

func loadPkg(t *testing.T, path string) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, path)
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("load %s: %v", path, err)
	}
	return pkgs[0]
}

type fixtureImporter struct{ byPath map[string]*types.Package }

func importerFor(roots ...*packages.Package) *fixtureImporter {
	idx := &fixtureImporter{byPath: map[string]*types.Package{}}
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

func (i *fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := i.byPath[path]; ok {
		return pkg, nil
	}
	return nil, &missingPkgError{path}
}

type missingPkgError struct{ path string }

func (e *missingPkgError) Error() string { return "fixtureImporter: no package " + e.path }

// scopeInput assembles a full Input for the scope fixture, including the
// Fset and Pkgs that only scope detection reads.
func scopeInput(t *testing.T, pkg *types.Package, fset *token.FileSet, pkgs []*packages.Package, all []*graph.Provider, roots []load.RootDecl, scopes []load.ScopeDecl) Input {
	t.Helper()
	scope := map[string]bool{}
	for _, c := range all {
		scope[c.Pkg] = true
	}
	return Input{
		Spec:       &load.Spec{Roots: roots, Scopes: scopes},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      scope,
		Fset:       fset,
		Pkgs:       pkgs,
	}
}

func rootDecl(pkg *types.Package, name string) load.RootDecl {
	return load.RootDecl{Key: ptrKey(pkg, name), Type: ptrType(pkg, name), Pos: token.Position{Filename: "spec.go", Line: 9}}
}

func scopeDecl(pkg *types.Package, impl, iface string) load.ScopeDecl {
	return load.ScopeDecl{
		Impl: ptrKey(pkg, impl), ImplType: ptrType(pkg, impl),
		Iface: namedKey(pkg, iface), IfaceType: namedType(pkg, iface),
		Pos: token.Position{Filename: "spec.go", Line: 10},
	}
}

func diagText(diags []Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.String())
		b.WriteString("\n")
	}
	return b.String()
}

// TestScopeMembership is the core classification: the key's dependants
// belong to the scope, the logger they all share does not.
func TestScopeMembership(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Scopes) != 1 {
		t.Fatalf("got %d scopes, want 1", len(resolved.Scopes))
	}
	s := resolved.Scopes[0]
	if s.KeyKey.String() != "example.com/scoped.RoomKey" {
		t.Fatalf("scope key = %s", s.KeyKey)
	}
	if s.Linger != 30*time.Second || s.Max != 10_000 {
		t.Fatalf("scope policy = linger %s max %d, want the declared defaults", s.Linger, s.Max)
	}

	var members []string
	for _, n := range s.Order {
		members = append(members, n.Key.String())
	}
	want := []string{"*example.com/scoped.RoomLog", "*example.com/scoped.Room"}
	if strings.Join(members, ",") != strings.Join(want, ",") {
		t.Fatalf("members = %v, want %v (construction order)", members, want)
	}
	// The logger is reached through the scope but doesn't vary with the
	// key, so it stays one app-level instance.
	var singletons []string
	for _, n := range resolved.Order {
		singletons = append(singletons, n.Key.String())
	}
	if strings.Join(singletons, ",") != "*example.com/scoped.Logger,*example.com/scoped.Server" {
		t.Fatalf("singletons = %v", singletons)
	}
	if s.Order[0].ScopeLevel != 1 || s.Order[1].ScopeLevel != 2 {
		t.Fatalf("scope levels = %d, %d; want 1, 2", s.Order[0].ScopeLevel, s.Order[1].ScopeLevel)
	}
}

func TestScopePolicyOptions(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	decl := scopeDecl(pkg, "Room", "Rooms")
	decl.Linger, decl.LingerSet = 5*time.Second, true
	decl.Max, decl.MaxSet = 42, true

	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.ScopeDecl{decl}))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if s := resolved.Scopes[0]; s.Linger != 5*time.Second || s.Max != 42 {
		t.Fatalf("policy = linger %s max %d", s.Linger, s.Max)
	}
}

func TestWideningDiagnostic(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Widener")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	for _, want := range []string{
		"is scoped, but *example.com/scoped.Widener is a singleton that depends on it",
		"needed by *example.com/scoped.Widener",
		"root",
		"depend on the accessor instead",
		"example.com/scoped.Rooms",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestCrossScopeDiagnostic(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{
			scopeDecl(pkg, "Nested", "Nesteds"),
			scopeDecl(pkg, "Tenant", "Tenants"),
			scopeDecl(pkg, "Room", "Rooms"),
		}))

	msg := diagText(diags)
	for _, want := range []string{
		"are in different scopes",
		"is keyed by example.com/scoped.RoomKey",
		"is keyed by example.com/scoped.TenantKey",
		"Nested scopes are deliberately not supported",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestExtractorCycleDiagnostic(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "Extractor", "Extractors")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "extractor depends on *example.com/scoped.Decoder, which is itself scoped") {
		t.Fatalf("diagnostic missing the extractor-cycle message:\n%s", msg)
	}
	if !strings.Contains(msg, "runs before\n  any instance exists") {
		t.Fatalf("diagnostic missing the explanation:\n%s", msg)
	}
}

func TestUndeclaredScopeDiagnostic(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "UndeclaredHolder")}, nil))

	msg := diagText(diags)
	for _, want := range []string{
		"declares a ScopeKey method but no servo.Scoped declares it",
		"type Undeclareds interface {",
		"Acquire(ctx context.Context) (*Undeclared, func(), error)",
		"servo.Scoped[*example.com/scoped.Undeclared, example.com/scoped.Undeclareds](),",
		"Or delete the ScopeKey method",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestUndeclaredScopeFindsAnExistingAccessor: the likeliest way to reach
// this diagnostic is to have written everything except the servo.Scoped
// line, in which case telling the reader to declare the accessor
// interface hands them a redeclaration error.
func TestUndeclaredScopeFindsAnExistingAccessor(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "DeclaredHolder")}, nil))

	msg := diagText(diags)
	if strings.Contains(msg, "type Declareds interface {") {
		t.Fatalf("diagnostic told the reader to declare an interface their package already has:\n%s", msg)
	}
	for _, want := range []string{
		"scoped.Declareds already has the shape the generated accessor satisfies",
		"servo.Scoped[*example.com/scoped.Declared, example.com/scoped.Declareds](),",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestUndeclaredScopeNamesTheInterfaceItFound: the prose and the
// pasteable servo.Scoped line have to agree. qualifiedIfaceName used to
// ignore the name it was handed and re-derive one from the scoped type,
// which is invisible whenever the accessor happens to be Name+"s" and
// wrong the moment it is not.
func TestUndeclaredScopeNamesTheInterfaceItFound(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "BlobHolder")}, nil))

	msg := diagText(diags)
	if !strings.Contains(msg, "scoped.BlobPool already has the shape") {
		t.Fatalf("diagnostic did not name the interface it found:\n%s", msg)
	}
	if !strings.Contains(msg, "servo.Scoped[*example.com/scoped.Blob, example.com/scoped.BlobPool](),") {
		t.Fatalf("pasteable line does not name BlobPool:\n%s", msg)
	}
	if strings.Contains(msg, "example.com/scoped.Blobs") {
		t.Fatalf("diagnostic invented a Blobs interface that does not exist:\n%s", msg)
	}
}

// TestWideningNamesOnlyReachingAccessors: Room and Lobby share RoomKey,
// so they are one scope with two roots — but only Room reaches RoomLog.
// Advising the reader to acquire through Lobbies would not typecheck.
func TestWideningNamesOnlyReachingAccessors(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "LogHolder")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "Lobby", "Lobbies")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "Depend on example.com/scoped.Rooms instead") {
		t.Fatalf("diagnostic did not name the accessor that reaches RoomLog:\n%s", msg)
	}
	if strings.Contains(msg, "Lobbies") || strings.Contains(msg, "scoped.Lobby ") {
		t.Fatalf("diagnostic named Lobby/Lobbies, which do not reach RoomLog:\n%s", msg)
	}
}

func TestScopedWithoutScopeKeyDiagnostic(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "NoScopeKey", "NoScopeKeys")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "declares a scope, but *example.com/scoped.NoScopeKey has no ScopeKey method") {
		t.Fatalf("diagnostic missing the missing-method message:\n%s", msg)
	}
	if !strings.Contains(msg, "The receiver must be unnamed") {
		t.Fatalf("diagnostic missing the unnamed-receiver requirement:\n%s", msg)
	}
}

func TestScopeKeyRequestedOutsideItsScope(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "KeyEater")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "is a scope key and is not resolvable outside its scope") {
		t.Fatalf("diagnostic missing the scope-key message:\n%s", msg)
	}
}

func TestAccessorInterfaceValidation(t *testing.T) {
	for _, tc := range []struct {
		iface   string
		wantMsg string
	}{
		{"BadMethod", "it declares Reset, which the generated accessor does not have"},
		{"BadAcquire", "the Acquire method must return exactly three results"},
		{"BadAcquireParams", "the Acquire method must take exactly one parameter, a context.Context"},
		{"BadAcquireType", "but this scope hands out *example.com/scoped.Room"},
		{"BadAcquireCleanup", "the Acquire method's second result must be func(), the release closure"},
		{"BadAcquireError", "the Acquire method's third result must be error"},
		{"BadStats", "the Stats method must return exactly one result, servo.ScopeStats"},
		{"BadStatsParams", "the Stats method takes no parameters"},
	} {
		t.Run(tc.iface, func(t *testing.T) {
			pkg, fset, pkgs, all := checkScopeFixture(t)
			decl := scopeDecl(pkg, "Room", "Rooms")
			decl.Iface, decl.IfaceType = namedKey(pkg, tc.iface), namedType(pkg, tc.iface)

			_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
				[]load.RootDecl{rootDecl(pkg, "Logger")}, []load.ScopeDecl{decl}))

			msg := diagText(diags)
			if !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("diagnostic missing %q:\n%s", tc.wantMsg, msg)
			}
			if !strings.Contains(msg, "Declare either or both, and nothing else.") {
				t.Fatalf("diagnostic missing the accepted shapes:\n%s", msg)
			}
		})
	}
}

// TestConflictingScopePolicy covers two declarations that share a key type
// — and therefore one registry — disagreeing about its policy.
func TestConflictingScopePolicy(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	room := scopeDecl(pkg, "Room", "Rooms")
	room.Linger, room.LingerSet = time.Second, true
	room.Max, room.MaxSet = 10, true
	lobby := scopeDecl(pkg, "Lobby", "Lobbies")
	lobby.Linger, lobby.LingerSet = 2*time.Second, true
	lobby.Max, lobby.MaxSet = 20, true

	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")}, []load.ScopeDecl{room, lobby}))

	msg := diagText(diags)
	if !strings.Contains(msg, "conflicting servo.Linger for scope example.com/scoped.RoomKey") {
		t.Fatalf("diagnostic missing the linger conflict:\n%s", msg)
	}
	if !strings.Contains(msg, "conflicting servo.Max for scope example.com/scoped.RoomKey") {
		t.Fatalf("diagnostic missing the max conflict:\n%s", msg)
	}
}

// TestTwoRootsShareOneScope is the multi-root case: two Scoped
// declarations whose extractors return the same key type get one registry,
// with both types in every entry.
func TestTwoRootsShareOneScope(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "Lobby", "Lobbies")}))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Scopes) != 1 {
		t.Fatalf("got %d scopes, want 1 — both roots key on RoomKey", len(resolved.Scopes))
	}
	if got := len(resolved.Scopes[0].Roots); got != 2 {
		t.Fatalf("got %d roots, want 2", got)
	}
	var members []string
	for _, n := range resolved.Scopes[0].Order {
		members = append(members, n.Key.String())
	}
	if len(members) != 3 {
		t.Fatalf("members = %v, want RoomLog, Room and Lobby all in one entry", members)
	}
}

// TestNoScopesLeavesResolvedUntouched pins the additive claim at the
// resolver level: with nothing declared, nothing about the plan changes.
func TestNoScopesLeavesResolvedUntouched(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")}, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Scopes) != 0 {
		t.Fatalf("got %d scopes, want none", len(resolved.Scopes))
	}
	for _, n := range resolved.Order {
		if n.Scoped() || n.Kind != NodeProvider {
			t.Fatalf("node %s is not a plain singleton", n.Key)
		}
	}
}

// TestResolveWithoutFsetDoesNotPanic covers the documented optionality of
// Input.Fset: a caller with no loaded module (servo's own hand-built test
// pipelines) must still resolve a scope-free graph.
func TestResolveWithoutFsetDoesNotPanic(t *testing.T) {
	pkg, _, _, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, nil, nil, all, []load.RootDecl{rootDecl(pkg, "Logger")}, nil)
	in.Fset, in.Pkgs = nil, nil
	if _, diags := Resolve(in); len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
}

// TestSecondDeclarationSuppliesTheOption covers the merge path where the
// first declaration left an option unset and a later one fills it in —
// there is nothing to conflict with, so the scope simply takes it.
func TestSecondDeclarationSuppliesTheOption(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	room := scopeDecl(pkg, "Room", "Rooms") // no options at all
	lobby := scopeDecl(pkg, "Lobby", "Lobbies")
	lobby.Linger, lobby.LingerSet = 7*time.Second, true
	lobby.Max, lobby.MaxSet = 77, true

	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.ScopeDecl{room, lobby}))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if s := resolved.Scopes[0]; s.Linger != 7*time.Second || s.Max != 77 {
		t.Fatalf("policy = linger %s max %d, want the second declaration's values", s.Linger, s.Max)
	}
}

// TestSameOptionTwiceIsNotAConflict: two declarations may repeat the
// identical policy. Only a disagreement is an error.
func TestSameOptionTwiceIsNotAConflict(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	room := scopeDecl(pkg, "Room", "Rooms")
	room.Linger, room.LingerSet = time.Second, true
	room.Max, room.MaxSet = 5, true
	lobby := scopeDecl(pkg, "Lobby", "Lobbies")
	lobby.Linger, lobby.LingerSet = time.Second, true
	lobby.Max, lobby.MaxSet = 5, true

	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.ScopeDecl{room, lobby}))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if s := resolved.Scopes[0]; s.Linger != time.Second || s.Max != 5 {
		t.Fatalf("policy = linger %s max %d", s.Linger, s.Max)
	}
}

// TestScopeKeyWithABadShapeIsReportedAtItsDeclaration covers the path
// where FindScopeKey returns an error rather than nil: the type carries a
// ScopeKey the generator cannot honour.
func TestScopeKeyWithABadShapeIsReportedAtItsDeclaration(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "BadKey", "BadKeys")}))

	if msg := diagText(diags); !strings.Contains(msg, "not a defined type") {
		t.Fatalf("diagnostic missing the key-type rejection:\n%s", msg)
	}
}

// TestNamedReceiverIsRejectedAtGenerateTime is the generator half of the
// blank-receiver rule — servo-vet catches it in the editor, and this
// catches it before anything is emitted.
func TestNamedReceiverIsRejectedAtGenerateTime(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "NamedRecv", "NamedRecvs")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "must not name its receiver") {
		t.Fatalf("diagnostic missing the unnamed-receiver rule:\n%s", msg)
	}
	if !strings.Contains(msg, "calls it on a typed nil") {
		t.Fatalf("diagnostic missing the reason:\n%s", msg)
	}
}

// TestWideningOnATransitiveMember covers the message variant for a scoped
// node no accessor exposes: there is no interface to point the consumer
// at, so the advice has to be phrased differently.
func TestWideningOnATransitiveMember(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "LogHolder")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "*example.com/scoped.RoomLog is scoped") {
		t.Fatalf("diagnostic missing the widening claim:\n%s", msg)
	}
	// The generic "depend on the scope's accessor interface" wording named
	// no interface, and following it literally did not typecheck: the only
	// accessor here hands out *Room, not the *RoomLog being captured. The
	// message has to name the entrance and the interface.
	for _, want := range []string{
		"*example.com/scoped.RoomLog has no accessor of its own",
		"*example.com/scoped.Room reaches it",
		"Depend on example.com/scoped.Rooms instead",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "depend on the scope's accessor interface instead of on") {
		t.Fatalf("diagnostic fell back to advice that names no interface:\n%s", msg)
	}
}

// TestStrayScopeKeyIsRejected covers a hole in scope membership: *Widget
// takes RoomKey but is reachable only from the TenantKey scope, so each
// scope's own fixpoint classifies it as a member of neither. It used to
// emit as a singleton with the key argument silently dropped.
func TestStrayScopeKeyIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "TenantHolder", "TenantHolders")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "*example.com/scoped.Widget") || !strings.Contains(msg, "which is a scope key") {
		t.Fatalf("diagnostic missing the stray-key message:\n%s", msg)
	}
}

// TestNodeClaimedByTwoScopes covers a node both scopes consider theirs.
// It used to be assigned to whichever was processed first, leaving the
// other scope's entry silently without it.
func TestNodeClaimedByTwoScopes(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "RoomBridge", "RoomBridges"), scopeDecl(pkg, "StolenRoot", "StolenRoots")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "belongs to two scopes at once") {
		t.Fatalf("diagnostic missing the double-claim message:\n%s", msg)
	}
}

// TestScopedRootIsRejected covers a scoped type declared as a root. A
// root is the most singleton thing there is, so that is widening with the
// App as the consumer. It used to be dropped without a word.
func TestScopedRootIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Room")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "declares a scoped type as a root") {
		t.Fatalf("diagnostic missing the scoped-root message:\n%s", msg)
	}
}

// TestBindingAnAccessorIsRejected covers the one scope mistake that was
// silently wrong at runtime rather than a compile error: an Override
// naming a scope accessor was ignored, so a test App went on exercising
// the real scope while its spec file said otherwise.
func TestBindingAnAccessorIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})
	in.ExtraBinds = []load.BindDecl{{
		Iface: namedKey(pkg, "Rooms"), IfaceType: namedType(pkg, "Rooms"),
		Concrete: ptrKey(pkg, "Logger"), ConcreteType: ptrType(pkg, "Logger"),
	}}

	_, diags := Resolve(in)
	msg := diagText(diags)
	if !strings.Contains(msg, "is a scope accessor interface and cannot be bound or overridden") {
		t.Fatalf("diagnostic missing the bound-accessor message:\n%s", msg)
	}
}

// TestUnrelatedScopeKeyMethodIsIgnored guards zero impact on apps that
// declare no scopes: ScopeKey is an ordinary method name, and a type that
// merely has one must not fail generation for a module with no scopes at
// all.
func TestUnrelatedScopeKeyMethodIsIgnored(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "PlainHolder")}, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Order) != 2 {
		t.Fatalf("got %d nodes, want PlainScopeKey and PlainHolder", len(resolved.Order))
	}
}

// TestValueTypedScopeIsRejected covers a value-typed scoped node.
// Detection used to accept one the emitter cannot express, producing a
// generated file that does not compile.
func TestValueTypedScopeIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	decl := load.ScopeDecl{
		Impl: namedKey(pkg, "ValueScoped"), ImplType: namedType(pkg, "ValueScoped"),
		Iface: namedKey(pkg, "ValueScopeds"), IfaceType: namedType(pkg, "ValueScopeds"),
		Pos: token.Position{Filename: "spec.go", Line: 10},
	}
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")}, []load.ScopeDecl{decl}))

	if msg := diagText(diags); !strings.Contains(msg, "first type argument must be a pointer") {
		t.Fatalf("diagnostic missing the pointer requirement:\n%s", msg)
	}
}

// TestExtractorMayTakeAnotherScopesAccessor: the extractor-cycle check
// used to reject the very edge every other scope diagnostic recommends.
// An accessor is an App field, not an instance.
func TestExtractorMayTakeAnotherScopesAccessor(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	resolved, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "AccessorExtractor", "AccessorExtractors")}))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Scopes) != 2 {
		t.Fatalf("got %d scopes, want 2", len(resolved.Scopes))
	}
}

// TestSelfAcquiringExtractorIsRejected is the one accessor edge that is
// not a way out: Acquire calls the extractor, so acquiring from inside it
// recurses without bound.
func TestSelfAcquiringExtractorIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "SelfAcquirer", "SelfAcquirers")}))

	if msg := diagText(diags); !strings.Contains(msg, "its own scope's accessor") {
		t.Fatalf("diagnostic missing the self-acquire message:\n%s", msg)
	}
}

// TestMalformedExtractorIsCaughtEvenUndeclared: the leniency that keeps
// scope-free modules working also swallowed the most dangerous mistake
// there is — an extractor with no error result, which makes a missing key
// the zero key.
func TestMalformedExtractorIsCaughtEvenUndeclared(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "NoErrorHolder")}, nil))

	if msg := diagText(diags); !strings.Contains(msg, "declares a ScopeKey method but no servo.Scoped declares it") {
		t.Fatalf("an extractor missing its error result was silently a singleton:\n%s", msg)
	}
}

// TestScopeKeyOutsideTheMainModuleIsIgnored: a dependency whose own types
// happen to have a method of that name is not something the user can
// declare a scope for or delete the method from.
func TestScopeKeyOutsideTheMainModuleIsIgnored(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all, []load.RootDecl{rootDecl(pkg, "NoErrorHolder")}, nil)
	in.Scope = map[string]bool{} // nothing is in the main module

	// It still fails — the type genuinely cannot be constructed without a
	// key — but as an ordinary missing provider, not with a scope
	// diagnostic whose every suggested fix is in a module the user does
	// not own.
	_, diags := Resolve(in)
	if msg := diagText(diags); strings.Contains(msg, "declares a ScopeKey method") {
		t.Fatalf("a third-party ScopeKey produced a scope diagnostic:\n%s", msg)
	}
}

// TestProviderForAnAccessorInterfaceIsRejected: the accessor
// short-circuits ahead of provider selection, so such a constructor is an
// accepted candidate that would never be called — dead code with nothing
// in the spec file to hint at it.
func TestProviderForAnAccessorInterfaceIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "DirectRooms")}))

	if msg := diagText(diags); !strings.Contains(msg, "which is a scope accessor interface") {
		t.Fatalf("diagnostic missing the accessor-provider message:\n%s", msg)
	}
}

// TestAccessorDeclaredAsARootIsRejected covers the second half of the
// scoped-root check: an accessor is generated code, not a node a root can
// pull into the graph, so a Root naming one produced nothing at all.
func TestAccessorDeclaredAsARootIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	roots := []load.RootDecl{{
		Key: namedKey(pkg, "Rooms"), Type: namedType(pkg, "Rooms"),
		Pos: token.Position{Filename: "spec.go", Line: 9},
	}}
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all, roots,
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "declares a scope accessor as a root") {
		t.Fatalf("diagnostic missing the accessor-root message:\n%s", msg)
	}
	if !strings.Contains(msg, "generated code, not a node servo constructs") {
		t.Fatalf("diagnostic missing the reason:\n%s", msg)
	}
}

// TestSuggestedAccessorKeepsTypeArguments: a suggested
// `Acquire(ctx) (*Box, ...)` for a *Box[string] does not compile, which
// makes the snippet worse than no snippet.
func TestSuggestedAccessorKeepsTypeArguments(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "BoxHolder")}, nil))

	msg := diagText(diags)
	if !strings.Contains(msg, "*GenericBox[string]") {
		t.Fatalf("suggested accessor dropped the type arguments:\n%s", msg)
	}
}

// TestPromotedScopeKeyIsExplained: saying "has no ScopeKey method" about a
// type where x.ScopeKey(ctx) compiles is not a useful thing to be told.
func TestPromotedScopeKeyIsExplained(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "PromotedKey", "PromotedKeys")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "has no ScopeKey method") {
		t.Fatalf("diagnostic missing the missing-method claim:\n%s", msg)
	}
	if !strings.Contains(msg, "through an embedded field") {
		t.Fatalf("diagnostic does not explain the promotion:\n%s", msg)
	}
}

// TestStatsMustReturnScopeStats covers the accessor check's other
// non-servo-type branch.
func TestStatsMustReturnScopeStats(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	decl := scopeDecl(pkg, "Room", "Rooms")
	decl.Iface, decl.IfaceType = namedKey(pkg, "StatsNotScopeStats"), namedType(pkg, "StatsNotScopeStats")

	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")}, []load.ScopeDecl{decl}))

	if msg := diagText(diags); !strings.Contains(msg, "must return exactly one result, servo.ScopeStats") {
		t.Fatalf("diagnostic missing the Stats result requirement:\n%s", msg)
	}
}

// TestExtractorReceiverCheckSkipsPackagesWithoutSyntax: when the declaring
// package was loaded without syntax there is nothing to inspect, and
// generation proceeds rather than blocking on a load-mode detail.
func TestExtractorReceiverCheckSkipsPackagesWithoutSyntax(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	stripped := []*packages.Package{{
		Name: pkgs[0].Name, PkgPath: pkgs[0].PkgPath,
		Types: pkgs[0].Types, Fset: pkgs[0].Fset,
	}}
	// NamedRecv would be rejected if its declaration were readable.
	_, diags := Resolve(scopeInput(t, pkg, fset, stripped, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "NamedRecv", "NamedRecvs")}))

	if msg := diagText(diags); strings.Contains(msg, "must not name its receiver") {
		t.Fatalf("the receiver check ran against a package with no syntax:\n%s", msg)
	}
}

// TestAccessorMayDeclareBothMethods is the accepted shape the rejection
// tests are the inverse of — and the one both example modules use.
func TestAccessorMayDeclareBothMethods(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	decl := scopeDecl(pkg, "Room", "Rooms")
	decl.Iface, decl.IfaceType = namedKey(pkg, "RoomsWithStats"), namedType(pkg, "RoomsWithStats")

	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")}, []load.ScopeDecl{decl}))
	if len(diags) > 0 {
		t.Fatalf("an accessor declaring Acquire and Stats was rejected:\n%s", diagText(diags))
	}
}

// TestStrayKeyAcrossTwoScopes covers the other half of the stray-key
// check: a node that *is* in a scope, holding a different scope's key.
// The singleton half is TestStrayScopeKeyIsRejected.
func TestStrayKeyAcrossTwoScopes(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Logger")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "StrayAcross", "StrayAcrosses")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "another scope's key") {
		t.Fatalf("diagnostic missing the cross-scope stray-key message:\n%s", msg)
	}
	if !strings.Contains(msg, "Nested scopes are deliberately not supported") {
		t.Fatalf("diagnostic missing the rejection's reason:\n%s", msg)
	}
}

// TestTransitivelyScopedRootHasNoAccessorToName: a scoped node no
// servo.Scoped exposes has no accessor interface, so the "delete this
// Root" message has to phrase its alternative generically.
func TestTransitivelyScopedRootHasNoAccessorToName(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	_, diags := Resolve(scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "RoomLog"), rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")}))

	msg := diagText(diags)
	if !strings.Contains(msg, "declares a scoped type as a root") {
		t.Fatalf("diagnostic missing the scoped-root message:\n%s", msg)
	}
	if !strings.Contains(msg, "that scope's accessor interface") {
		t.Fatalf("diagnostic should fall back to a generic phrase for a node no accessor exposes:\n%s", msg)
	}
}
