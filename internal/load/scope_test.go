package load

import (
	"strings"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

// scopeModule writes a module whose spec body is specBody, so each case
// differs only in the servo.Build(...) call it declares.
func scopeModule(t *testing.T, module, specBody string) string {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module "+module+"\n\ngo 1.25.0\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "chat/chat.go", `package chat

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type RoomKey string

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }

func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", servo.ErrNoScopeKey }

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`)
	mustWriteFile(t, dir, "spec/spec.go", "//go:build servoinject\n\npackage spec\n\n"+specBody)
	runGoModTidy(t, dir)
	return dir
}

func findSpecIn(t *testing.T, dir string) (*Spec, error) {
	t.Helper()
	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return FindSpec(loaded)
}

func TestParseScopedMarker(t *testing.T) {
	dir := scopeModule(t, "example.com/scopedspec", `
import (
	"time"

	"example.com/scopedspec/chat"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*chat.Server](),
		servo.Scoped[*chat.Room, chat.Rooms](
			servo.Linger(90*time.Second),
			servo.Max(1_500),
		),
	)
}
`)
	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Scopes) != 1 {
		t.Fatalf("got %d scope declarations, want 1", len(spec.Scopes))
	}
	d := spec.Scopes[0]
	if d.Impl.String() != "*example.com/scopedspec/chat.Room" {
		t.Fatalf("impl = %s", d.Impl)
	}
	if d.Iface.String() != "example.com/scopedspec/chat.Rooms" {
		t.Fatalf("iface = %s", d.Iface)
	}
	if !d.LingerSet || d.Linger != 90*time.Second {
		t.Fatalf("linger = %s (set=%v)", d.Linger, d.LingerSet)
	}
	if !d.MaxSet || d.Max != 1500 {
		t.Fatalf("max = %d (set=%v)", d.Max, d.MaxSet)
	}
}

// TestScopedDefaultsWhenOptionsOmitted pins the values a declaration with
// no options gets, and that they are the servo package's own constants
// rather than a second, drifting copy.
func TestScopedDefaultsWhenOptionsOmitted(t *testing.T) {
	dir := scopeModule(t, "example.com/scopeddefaults", `
import (
	"example.com/scopeddefaults/chat"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*chat.Server](),
		servo.Scoped[*chat.Room, chat.Rooms](),
	)
}
`)
	spec, err := findSpecIn(t, dir)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	d := spec.Scopes[0]
	if d.LingerSet || d.MaxSet {
		t.Fatal("options reported as set when none were declared")
	}
	if d.EffectiveLinger() != servo.DefaultLinger {
		t.Fatalf("default linger = %s, want servo.DefaultLinger (%s)", d.EffectiveLinger(), servo.DefaultLinger)
	}
	if d.EffectiveMax() != servo.DefaultMax {
		t.Fatalf("default max = %d, want servo.DefaultMax (%d)", d.EffectiveMax(), servo.DefaultMax)
	}
}

func TestScopedRejections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		build   string
		wantMsg string
	}{
		{
			name:    "iface is not an interface",
			build:   "servo.Scoped[*chat.Room, chat.Room]()",
			wantMsg: "second type argument must be an interface",
		},
		{
			name:    "impl is an interface",
			build:   "servo.Scoped[chat.Rooms, chat.Rooms]()",
			wantMsg: "first type argument must be the concrete scoped type",
		},
		{
			name:    "empty accessor interface",
			build:   "servo.Scoped[*chat.Room, any]()",
			wantMsg: "declares no methods",
		},
		{
			name:    "non-constant linger",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(vary()))",
			wantMsg: "must be a constant expression",
		},
		{
			name:    "negative linger",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(-1))",
			wantMsg: "must not be negative",
		},
		{
			name:    "zero max",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Max(0))",
			wantMsg: "must be positive",
		},
		{
			name:    "duplicate linger",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(1), servo.Linger(2))",
			wantMsg: "servo.Linger declared twice",
		},
		{
			name:    "duplicate max",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Max(1), servo.Max(2))",
			wantMsg: "servo.Max declared twice",
		},
		{
			name:    "unknown option",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Root[*chat.Room]())",
			wantMsg: "is not a scope option",
		},
		{
			name:    "option is not a call",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](opt)",
			wantMsg: "must be servo.Linger(...) or servo.Max(...) calls",
		},
		{
			name:    "option from another package",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](notAnOption())",
			wantMsg: "must be servo.Linger(...) or servo.Max(...) calls",
		},
		{
			name:    "wrong option arity",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](servo.Bind[chat.Rooms, *chat.Room]())",
			wantMsg: "is not a scope option",
		},
		{
			name:    "duplicate scoped type",
			build:   "servo.Scoped[*chat.Room, chat.Rooms](), servo.Scoped[*chat.Room, chat.Rooms]()",
			wantMsg: "declared twice",
		},
		{
			name:    "linger at the top level",
			build:   "servo.Linger(1)",
			wantMsg: "is a scope option, not a Build marker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scopeModule(t, "example.com/scopedbad", `
import (
	"example.com/scopedbad/chat"
	"github.com/okian/servo/v3/servo"
)

var opt servo.ScopeOption

func vary() int { return 1 }

func notAnOption() servo.ScopeOption { return opt }

func Wire() {
	servo.Build(
		servo.Root[*chat.Server](),
		`+tc.build+`,
	)
}
`)
			_, err := findSpecIn(t, dir)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestTwoScopesOneAccessorInterface covers the collision that would
// otherwise surface as a generated accessor no consumer can tell apart.
func TestTwoScopesOneAccessorInterface(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/twoscopes\n\ngo 1.25.0\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "chat/chat.go", `package chat

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type RoomKey string

type Anything interface{ Acquire(ctx context.Context) (*Room, func(), error) }

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }

func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", servo.ErrNoScopeKey }

type Lobby struct{}

func NewLobby(k RoomKey) *Lobby { return &Lobby{} }

func (_ *Lobby) ScopeKey(ctx context.Context) (RoomKey, error) { return "", servo.ErrNoScopeKey }
`)
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/twoscopes/chat"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Scoped[*chat.Room, chat.Anything](),
		servo.Scoped[*chat.Lobby, chat.Anything](),
	)
}
`)
	runGoModTidy(t, dir)

	_, err := findSpecIn(t, dir)
	if err == nil || !strings.Contains(err.Error(), "one accessor interface cannot stand for two scoped types") {
		t.Fatalf("err = %v, want the shared-accessor diagnostic", err)
	}
}

func TestEffectiveOptionsUseDeclaredValues(t *testing.T) {
	d := ScopeDecl{Linger: 7 * time.Second, LingerSet: true, Max: 3, MaxSet: true}
	if got := d.EffectiveLinger(); got != 7*time.Second {
		t.Fatalf("EffectiveLinger = %s, want the declared value", got)
	}
	if got := d.EffectiveMax(); got != 3 {
		t.Fatalf("EffectiveMax = %d, want the declared value", got)
	}
}

// TestLocalTypeString covers the renderer behind the paste-ready snippet
// in the not-an-interface diagnostic: it drops the import path, keeps the
// pointer, and keeps an instantiated generic's type arguments — a
// suggested `Acquire(ctx) (*Box, ...)` for a *Box[string] would not
// compile.
func TestLocalTypeString(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/localtype\n\ngo 1.25.0\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "chat/chat.go", `package chat

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type RoomKey string

type Box[T any] struct{}

func NewBox(k RoomKey) *Box[string] { return &Box[string]{} }

func (*Box[T]) ScopeKey(ctx context.Context) (RoomKey, error) { return "", servo.ErrNoScopeKey }
`)
	// Scoped's second type argument is a concrete type, so the diagnostic
	// prints the interface the user should declare — with *Box[string] in
	// its Acquire result.
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/localtype/chat"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Scoped[*chat.Box[string], chat.RoomKey](),
	)
}
`)
	runGoModTidy(t, dir)

	_, err := findSpecIn(t, dir)
	if err == nil {
		t.Fatal("expected the non-interface second type argument to be rejected")
	}
	for _, want := range []string{
		"must be an interface",
		"Acquire(ctx context.Context) (*Box[string], func(), error)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to contain %q", err, want)
		}
	}
}

// TestScopeOptionArityAndKind covers the option-parsing branches a
// well-formed spec never reaches.
func TestScopeOptionArityAndKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		build   string
		wantMsg string
	}{
		{"linger with no argument", "servo.Scoped[*chat.Room, chat.Rooms](noArgs())", "must be servo.Linger(...) or servo.Max(...) calls"},
		{"max from a variable", "servo.Scoped[*chat.Room, chat.Rooms](servo.Max(varInt))", "must be a constant expression"},
		{"linger from a variable", "servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(varDur))", "must be a constant expression"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scopeModule(t, "example.com/optarity", `
import (
	"time"

	"example.com/optarity/chat"
	"github.com/okian/servo/v3/servo"
)

var (
	varInt int
	varDur time.Duration
)

func noArgs() servo.ScopeOption { panic("never run") }

func Wire() {
	servo.Build(
		servo.Root[*chat.Server](),
		`+tc.build+`,
	)
}
`)
			_, err := findSpecIn(t, dir)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}
