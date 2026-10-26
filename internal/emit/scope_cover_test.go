package emit

import (
	"strings"
	"testing"
)

// extractorDepsSrc has a scope whose key extractor takes dependencies of
// its own — a singleton, and a second scope's accessor — and whose member
// is built with a cleanup func and can fail. The room key is the tenant's
// key and the session's room, joined.
const extractorDepsSrc = `
package app

import (
	"context"
	"errors"
)

type Sessions struct{ room RoomKey }

func NewSessions() *Sessions { return &Sessions{room: "lobby"} }

type TenantKey string

type Tenant struct{ Key TenantKey }

func NewTenant(k TenantKey) *Tenant { return &Tenant{Key: k} }
func (_ *Tenant) ScopeKey(ctx context.Context) (TenantKey, error) { return "acme", nil }

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}

type RoomKey string

type Room struct{ Key RoomKey }

var ErrClosed = errors.New("room closed")

func NewRoom(k RoomKey) (*Room, func(), error) {
	if k == "closed" {
		return nil, nil, ErrClosed
	}
	return &Room{Key: k}, func() {}, nil
}

func (_ *Room) ScopeKey(ctx context.Context, s *Sessions, tenants Tenants) (RoomKey, error) {
	t, release, err := tenants.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return RoomKey(string(t.Key) + "/" + string(s.room)), nil
}

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{ Rooms Rooms }

func NewServer(r Rooms) *Server { return &Server{Rooms: r} }
`

// emitExtractorDeps generates extractorDepsSrc's production file.
func emitExtractorDeps(t *testing.T) []byte {
	t.Helper()
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:  []string{"Server"},
		scopes: [][2]string{{"Room", "Rooms"}, {"Tenant", "Tenants"}},
	}, fixturePkg{"example.com/app", extractorDepsSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return out
}

// TestEmitExtractorDependencies: an extractor runs per acquire, long
// after New returned, so it reads its dependencies off the App: a
// singleton from its field, another scope's accessor from the field
// scopeSetup assigned. The singleton is one the scope borrows, as much as
// a member's dependency is. And a member with a cleanup func whose
// constructor can fail takes all three results.
func TestEmitExtractorDependencies(t *testing.T) {
	out := emitExtractorDeps(t)
	gen := string(out)
	for _, want := range []string{
		"key, err := zero.ScopeKey(ctx, s.app.sessions, s.app.tenants)",
		"room, roomCleanup, err := NewRoom(e.key)",
		`Members: []string{"*example.com/app.Room"}, Borrows: []string{"*example.com/app.Sessions"}},`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", extractorDepsSrc})
}

// TestEmitExtractorDependenciesRun acquires a room through the generated
// accessor: the extractor gets the singleton and the other scope's
// accessor, already set, and the room built is the one its key names.
func TestEmitExtractorDependenciesRun(t *testing.T) {
	runGenerated(t, "app", emitExtractorDeps(t), map[string]string{
		"app/app.go": extractorDepsSrc,
		"app/scope_test.go": `package app

import (
	"context"
	"testing"
)

func TestAcquireThroughExtractorDeps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(context.Background())
	room, release, err := app.server.Rooms.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if room.Key != "acme/lobby" {
		t.Errorf("acquired room %q, want acme/lobby", room.Key)
	}
}
`,
	})
}

// TestEmitAccessorForAnInterfaceLiteral: the accessor interface can be an
// alias of an interface literal, which has no name for the accessor type
// and its App field to be derived from. Both fall back to "scope" rather
// than to an empty identifier.
func TestEmitAccessorForAnInterfaceLiteral(t *testing.T) {
	const src = `
package app

import "context"

type RoomKey string

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms = interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`
	resolved, spec := buildFixture(t, fixtureSpec{roots: []string{"Server"}, scopes: [][2]string{{"Room", "Rooms"}}}, fixturePkg{"example.com/app", src})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	for _, want := range []string{
		"type scopeAccessor struct{ s *roomKeyScope }",
		"a.scope = scopeAccessor{s: a.roomKeyScope}",
		"server := NewServer(a.scope)",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("generated source missing %q\n---\n%s", want, out)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
}
