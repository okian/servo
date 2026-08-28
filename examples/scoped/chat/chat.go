// Package chat is the scoped half of this example: one Room per room
// name, shared by everyone in that room, torn down once the last of them
// leaves and the linger window closes.
package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/okian/servo/v3/servo"

	"example.com/servoscoped/logger"
)

// RoomKey identifies one room. It is a defined type, not a bare string,
// because scope identity is type identity: two scopes both keyed on
// `string` would be indistinguishable to the generator.
type RoomKey string

type ctxKey struct{}

// WithRoom is what a transport-edge middleware calls. servo deliberately
// ships no HTTP or gRPC adapter — putting the key in the context is the
// one part of this that belongs to the application.
func WithRoom(ctx context.Context, key RoomKey) context.Context {
	return context.WithValue(ctx, ctxKey{}, key)
}

// Rooms is the accessor interface. servo cannot emit a type into this
// package, so without an interface declared here there would be nothing
// for a consumer's constructor to depend on. The generated accessor
// satisfies it.
type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
	Stats() servo.ScopeStats
}

// Room is the scoped type. Everyone presenting the same RoomKey shares one
// of these; the messages field is exactly the in-memory state that would
// be lost if the instance were rebuilt per request.
type Room struct {
	key RoomKey
	log *RoomLog

	mu       sync.Mutex
	messages []string
	started  bool
	drained  bool
	stopped  bool
}

// Three keys with deliberately awkward behaviour, so the race suite can
// reach the paths that only a misbehaving component reaches. A real
// service would not have these; a test for the teardown machinery needs
// something that panics, something slow to drain, and something that
// fails to stop.
const (
	// PanicKey panics during construction.
	PanicKey RoomKey = "panic"
	// SlowDrainKey takes its time draining, so its instance is still
	// tearing down while the next acquire is admitted.
	SlowDrainKey RoomKey = "slow-drain"
	// FailStopKey returns an error from Stop.
	FailStopKey RoomKey = "fail-stop"
)

// NewRoom rejects a name no room may have. A constructor that can fail is
// what makes the entry's rollback path reachable: the RoomLog built a
// moment earlier has to be stopped and the key left out of the map, or the
// next acquire would find a half-built room.
func NewRoom(key RoomKey, rl *RoomLog) (*Room, error) {
	if strings.HasPrefix(string(key), "!") {
		return nil, fmt.Errorf("%w: %q", ErrBadRoomName, key)
	}
	if key == PanicKey {
		panic("chat: constructing " + key)
	}
	return &Room{key: key, log: rl}, nil
}

// ScopeKey extracts the key that decides which instance a caller gets.
//
// The receiver is unnamed, and must be unreachable from the body:
// generated code calls this on a typed nil, because the key has to be
// known before an instance can be chosen.
// Returning an error rather than the zero RoomKey is equally load-bearing
// — without it every keyless caller would silently share one room.
func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) {
	k, ok := ctx.Value(ctxKey{}).(RoomKey)
	if !ok || k == "" {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

func (r *Room) Key() RoomKey { return r.key }

func (r *Room) Init(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = true
	return nil
}

// Run holds the instance's own goroutine open for as long as the instance
// lives. It runs on the entry's context, which is New's context with
// cancellation stripped and a fresh cancel of its own — so one acquirer
// disconnecting does not kill a room other people are still in.
func (r *Room) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r *Room) Post(msg string) {
	r.mu.Lock()
	r.messages = append(r.messages, msg)
	r.mu.Unlock()
	r.log.Record(msg)
}

func (r *Room) Messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

func (r *Room) Drain(context.Context) error {
	if strings.HasPrefix(string(r.key), string(SlowDrainKey)) {
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drained = true
	return nil
}

func (r *Room) Stop(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.drained {
		return errStopBeforeDrain
	}
	r.stopped = true
	if r.key == FailStopKey {
		return ErrStopRefused
	}
	return nil
}

// Stopped reports whether this instance has already been torn down. A
// successful Acquire must never hand back one that has.
func (r *Room) Stopped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}

// RoomLog is scoped too, but only transitively: it declares no ScopeKey of
// its own and is never acquired directly. It depends on RoomKey, and that
// is what puts it in the same entry as the Room — one per room, torn down
// with it.
type RoomLog struct {
	key RoomKey
	log *logger.Logger

	mu      sync.Mutex
	pending []string
	flushed int
}

func NewRoomLog(key RoomKey, log *logger.Logger) *RoomLog {
	return &RoomLog{key: key, log: log}
}

func (l *RoomLog) Init(context.Context) error { return nil }

func (l *RoomLog) Record(msg string) {
	l.mu.Lock()
	l.pending = append(l.pending, msg)
	l.mu.Unlock()
}

// Flush runs at eviction, after the Room's Run goroutine has returned and
// before Stop, so a buffer filled while the room was live is written out
// rather than discarded.
func (l *RoomLog) Flush(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, msg := range l.pending {
		l.log.Printf("[%s] %s", l.key, msg)
	}
	l.flushed += len(l.pending)
	l.pending = nil
	return nil
}

func (l *RoomLog) Flushed() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushed
}
