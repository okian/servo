// Package session is the one part of this service's graph that isn't a
// singleton. Every other component is built once in New and held for the
// life of the process; a Session is built once per *user*, shared by every
// concurrent request that user has in flight, and torn down once they've
// been quiet for a while.
//
// See docs/tutorial/12-scoped-instances.md.
package session

import (
	"context"
	"log/slog"
	"sync"
	"uuid"

	"example.com/servoorders/config"
	"github.com/okian/servo/v3/servo"
)

// UserID is the scope key. It is a defined type, not a bare string,
// because scope identity is type identity: a second scope also keyed on
// `string` would be indistinguishable from this one to the generator.
type UserID string

type ctxKey struct{}

// WithUser is what the auth middleware calls once it knows who is asking.
// servo ships no HTTP adapter on purpose — putting the key in the context
// is the application's job, and it is the only line of transport code this
// whole feature needs.
func WithUser(ctx context.Context, id UserID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// Sessions is the accessor interface. servo can't emit a type into this
// package, so without an interface declared here there'd be nothing for
// api.New to depend on. The generated accessor satisfies it.
type Sessions interface {
	Acquire(ctx context.Context) (*Session, func(), error)
	Stats() servo.ScopeStats
}

// Session is per-user state that would be pointless to rebuild per
// request and wrong to share across users: the orders this person has
// looked at recently, plus a count of how much work they've caused.
type Session struct {
	id  UserID
	cfg *config.Config

	mu     sync.Mutex
	recent []uuid.UUID
	views  int
}

// New takes the key like any other dependency, and *config.Config like any
// other singleton. The config does not vary with the user, so it stays one
// shared instance rather than being rebuilt per session — servo works that
// out from the dependency edges, not from an annotation.
func New(id UserID, cfg *config.Config) *Session {
	return &Session{id: id, cfg: cfg}
}

// ScopeKey extracts the user this request belongs to.
//
// The receiver is unnamed, and must be. servo calls this on a typed nil,
// because the
// key has to be known before an instance can be chosen — there is no
// instance to call it on yet. Returning an error rather than the zero
// UserID is equally load-bearing: without it, every unauthenticated caller
// would silently share one session.
func (*Session) ScopeKey(ctx context.Context) (UserID, error) {
	id, ok := ctx.Value(ctxKey{}).(UserID)
	if !ok || id == "" {
		return "", servo.ErrNoScopeKey
	}
	return id, nil
}

func (s *Session) Init(context.Context) error {
	slog.Debug("session: opened", "user", string(s.id))
	return nil
}

// RecordView keeps the most recent SessionRecent order IDs, newest first.
func (s *Session) RecordView(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.views++
	s.recent = append([]uuid.UUID{id}, s.deduped(id)...)
	if len(s.recent) > s.cfg.SessionRecent {
		s.recent = s.recent[:s.cfg.SessionRecent]
	}
}

// deduped returns the existing list without id, so re-viewing an order
// moves it to the front instead of appearing twice.
func (s *Session) deduped(id uuid.UUID) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(s.recent))
	for _, existing := range s.recent {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}

// Recent is a copy, and a non-nil one even when empty: openapi.yaml
// declares `recent` as a required array, and a nil slice marshals to null.
func (s *Session) Recent() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(make([]uuid.UUID, 0, len(s.recent)), s.recent...)
}

// Flush runs at eviction, after the session's linger window has closed. It
// is where anything worth keeping leaves memory — here, one summary line;
// in a real service, whatever you'd want to survive the session ending.
func (s *Session) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	slog.Info("session: closed", "user", string(s.id), "views", s.views, "recent", len(s.recent))
	return nil
}

func (s *Session) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = nil
	return nil
}
