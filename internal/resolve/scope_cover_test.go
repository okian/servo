package resolve

import (
	"go/token"
	"regexp"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// scopeCoverSrc is a module with two scope keys and the shapes of scoped
// graph the widening and double-claim diagnostics word differently: a
// member two scope roots reach, a member only an extractor reaches, two
// nodes both scopes claim, and a capturing singleton two roots resolve to.
const scopeCoverSrc = `package app

import "context"

type RoomKey string
type TenantKey string

// Log is in the RoomKey scope only because it takes the key, and both
// Room and Lobby reach it; no accessor hands it out.
type Log struct{}

func NewLog(k RoomKey) *Log { return &Log{} }

type Room struct{}

func NewRoom(k RoomKey, l *Log) *Room                       { return &Room{} }
func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Lobby struct{}

func NewLobby(k RoomKey, l *Log) *Lobby                      { return &Lobby{} }
func (*Lobby) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Lobbies interface {
	Acquire(ctx context.Context) (*Lobby, func(), error)
}

type LogHolder struct{}

func NewLogHolder(l *Log) *LogHolder { return &LogHolder{} }

// Decoder is scoped only through Booth's extractor, which takes it.
type Decoder struct{}

func NewDecoder(k RoomKey) *Decoder { return &Decoder{} }

type Booth struct{}

func NewBooth(k RoomKey) *Booth                                         { return &Booth{} }
func (*Booth) ScopeKey(ctx context.Context, d *Decoder) (RoomKey, error) { return "", nil }

type Booths interface {
	Acquire(ctx context.Context) (*Booth, func(), error)
}

type DecoderHolder struct{}

func NewDecoderHolder(d *Decoder) *DecoderHolder { return &DecoderHolder{} }

// Chair and Desk take both keys and are reached from both scopes' roots,
// so each scope claims both of them.
type Chair struct{}

func NewChair(rk RoomKey, tk TenantKey) *Chair { return &Chair{} }

type Desk struct{}

func NewDesk(rk RoomKey, tk TenantKey) *Desk { return &Desk{} }

type Office struct{}

func NewOffice(k RoomKey, d *Desk, c *Chair) *Office          { return &Office{} }
func (*Office) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Offices interface {
	Acquire(ctx context.Context) (*Office, func(), error)
}

type Tenant struct{}

func NewTenant(k TenantKey, d *Desk, c *Chair) *Tenant           { return &Tenant{} }
func (*Tenant) ScopeKey(ctx context.Context) (TenantKey, error) { return "", nil }

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}

// Widener holds a Room, and is the only implementation of Runner.
type Runner interface{ Run(ctx context.Context) error }

type Widener struct{}

func NewWidener(r *Room) *Widener                { return &Widener{} }
func (*Widener) Run(ctx context.Context) error { return nil }
`

const scopeCoverPkg = "example.com/mod/app"

func scopeCoverDecl(m *modFixture, impl, iface string, line int) load.ScopeDecl {
	return load.ScopeDecl{
		Impl: graph.NewKey(m.ptr(scopeCoverPkg, impl), ""), ImplType: m.ptr(scopeCoverPkg, impl),
		Iface: graph.NewKey(m.typ(scopeCoverPkg, iface), ""), IfaceType: m.typ(scopeCoverPkg, iface),
		Pos: token.Position{Filename: "/mod/spec.go", Line: line},
	}
}

// TestWideningOnAMemberTwoEntrancesReach: the member has no accessor, so
// the advice names the scope entrances that lead to it — every one that
// does, joined, with the verb agreeing.
func TestWideningOnAMemberTwoEntrancesReach(t *testing.T) {
	m := checkModule(t, pkgSrc{scopeCoverPkg, scopeCoverSrc})
	in := m.input(scopeCoverPkg, m.ptr(scopeCoverPkg, "LogHolder"))
	in.Spec.Scopes = []load.ScopeDecl{scopeCoverDecl(m, "Room", "Rooms", 11), scopeCoverDecl(m, "Lobby", "Lobbies", 12)}

	msg := onlyDiagnostic(t, in).Message
	for _, want := range []string{
		"servo: *example.com/mod/app.Log is scoped, but *example.com/mod/app.LogHolder is a singleton that depends on it\n",
		"*example.com/mod/app.Log has no accessor of its own.",
		"scope because *example.com/mod/app.Room and *example.com/mod/app.Lobby reach it,",
		"Depend on example.com/mod/app.Rooms or example.com/mod/app.Lobbies instead,",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestWideningOnAMemberOnlyAnExtractorReaches: a type that is scoped only
// because a ScopeKey extractor takes it has no accessor and no entrance
// leading to it, so the advice cannot name one, and says what to do in
// general instead. The extractor taking a scoped type is its own mistake,
// reported beside it.
func TestWideningOnAMemberOnlyAnExtractorReaches(t *testing.T) {
	m := checkModule(t, pkgSrc{scopeCoverPkg, scopeCoverSrc})
	in := m.input(scopeCoverPkg, m.ptr(scopeCoverPkg, "DecoderHolder"))
	in.Spec.Scopes = []load.ScopeDecl{scopeCoverDecl(m, "Booth", "Booths", 11)}

	msg := mustFail(t, in)
	for _, want := range []string{
		"servo: *example.com/mod/app.Decoder is scoped, but *example.com/mod/app.DecoderHolder is a singleton that depends on it\n",
		"    - depend on the scope's accessor interface instead of on *example.com/mod/app.Decoder directly,\n      and call Acquire(ctx) per request\n",
		"servo: *example.com/mod/app.Booth's ScopeKey extractor depends on *example.com/mod/app.Decoder, which is itself scoped\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "has no accessor of its own") {
		t.Errorf("diagnostic names entrances that do not reach Decoder:\n%s", msg)
	}
}

// TestNodesClaimedByTwoScopesAreReportedInKeyOrder: the claims are
// collected from map iterations, so without a sort the two refusals would
// swap places from run to run. They come out by type, the same every time.
func TestNodesClaimedByTwoScopesAreReportedInKeyOrder(t *testing.T) {
	m := checkModule(t, pkgSrc{scopeCoverPkg, scopeCoverSrc})
	in := m.input(scopeCoverPkg)
	in.Spec.Scopes = []load.ScopeDecl{scopeCoverDecl(m, "Office", "Offices", 11), scopeCoverDecl(m, "Tenant", "Tenants", 12)}

	first := mustFail(t, in)
	chair := strings.Index(first, "servo: *example.com/mod/app.Chair belongs to two scopes at once")
	desk := strings.Index(first, "servo: *example.com/mod/app.Desk belongs to two scopes at once")
	if chair < 0 || desk < 0 || desk < chair {
		t.Fatalf("want both double claims, Chair's first:\n%s", first)
	}
	for i := range 20 {
		if got := mustFail(t, in); got != first {
			t.Fatalf("run %d reported\n%s\nwant\n%s", i, got, first)
		}
	}
}

// TestWideningSingletonReachedFromTwoRootsIsReportedOnce: two roots that
// resolve to the same capturing singleton — one through the interface it
// implements, one by its type — are one widening, rooted at the first.
func TestWideningSingletonReachedFromTwoRootsIsReportedOnce(t *testing.T) {
	m := checkModule(t, pkgSrc{scopeCoverPkg, scopeCoverSrc})
	in := m.input(scopeCoverPkg, m.typ(scopeCoverPkg, "Runner"), m.ptr(scopeCoverPkg, "Widener"))
	in.Spec.Scopes = []load.ScopeDecl{scopeCoverDecl(m, "Room", "Rooms", 12)}

	msg := onlyDiagnostic(t, in).Message
	for _, want := range []string{
		"servo: *example.com/mod/app.Room is scoped, but *example.com/mod/app.Widener is a singleton that depends on it\n",
		"change app.NewWidener's parameter from *example.com/mod/app.Room to example.com/mod/app.Rooms",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if !regexp.MustCompile(`(?m)^  root +/mod/spec\.go:10$`).MatchString(msg) {
		t.Errorf("diagnostic is not rooted at the first declaration, /mod/spec.go:10:\n%s", msg)
	}
}

// TestScopedRootDeclaredThroughAnInterface: the refusal quotes the root
// as the spec file wrote it, and adds what it resolved to, since the
// interface alone does not say which scoped type the reader is holding.
func TestScopedRootDeclaredThroughAnInterface(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	roots := []load.RootDecl{{Key: namedKey(pkg, "Chatter"), Type: namedType(pkg, "Chatter"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	msg := onlyDiagnostic(t, scopeInput(t, pkg, fset, pkgs, all, roots, []load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})).Message
	for _, want := range []string{
		"servo: servo.Root[example.com/scoped.Chatter] declares a scoped type as a root\n  spec.go:9\n  which resolves to *example.com/scoped.Room\n",
		"reachable through\n  example.com/scoped.Rooms,",
		"Delete this servo.Root.\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}
