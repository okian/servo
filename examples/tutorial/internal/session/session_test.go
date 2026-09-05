package session_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"uuid"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/session"
)

func newSession(t *testing.T, id session.UserID, cap int) *session.Session {
	t.Helper()
	s := session.New(id, session.NewSettings(session.Config{Recent: cap}), quietLogger())
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

// TestScopeKeyReadsTheContext covers both halves of the extractor
// contract: a key present, and a key absent. The second is the one that
// matters — returning the zero UserID instead of an error would silently
// give every anonymous caller the same session.
func TestScopeKeyReadsTheContext(t *testing.T) {
	// Called on a typed nil, exactly the way generated code calls it.
	var zero *session.Session

	want := session.UserID("alice")
	got, err := zero.ScopeKey(session.WithUser(context.Background(), want))
	if err != nil {
		t.Fatalf("ScopeKey: %v", err)
	}
	if got != want {
		t.Fatalf("ScopeKey = %q, want %q", got, want)
	}

	if _, err := zero.ScopeKey(context.Background()); !errors.Is(err, servo.ErrNoScopeKey) {
		t.Fatalf("ScopeKey with no key: err = %v, want servo.ErrNoScopeKey", err)
	}
	if _, err := zero.ScopeKey(session.WithUser(context.Background(), "")); !errors.Is(err, servo.ErrNoScopeKey) {
		t.Fatalf("ScopeKey with an empty key: err = %v, want servo.ErrNoScopeKey", err)
	}
}

func TestRecordViewKeepsNewestFirstAndCaps(t *testing.T) {
	s := newSession(t, "alice", 2)
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	s.RecordView(a)
	s.RecordView(b)
	s.RecordView(c)

	got := s.Recent()
	if len(got) != 2 || got[0] != c || got[1] != b {
		t.Fatalf("recent = %v, want [%v %v] — newest first, capped at 2", got, c, b)
	}
}

// TestRecordViewMovesARepeatToTheFront: viewing something twice should
// promote it, not duplicate it.
func TestRecordViewMovesARepeatToTheFront(t *testing.T) {
	s := newSession(t, "alice", 10)
	a, b := uuid.New(), uuid.New()

	s.RecordView(a)
	s.RecordView(b)
	s.RecordView(a)

	got := s.Recent()
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("recent = %v, want [%v %v]", got, a, b)
	}
}

func TestRecentReturnsACopy(t *testing.T) {
	s := newSession(t, "alice", 10)
	id := uuid.New()
	s.RecordView(id)

	got := s.Recent()
	got[0] = uuid.New()
	if s.Recent()[0] != id {
		t.Fatal("Recent handed out its own slice — a caller mutated the session's state")
	}
}

// TestTwoSessionsDoNotShareState is the property the scope exists to
// guarantee. servo's widening diagnostic exists because the version of
// this that fails — one shared instance — passes every single-user test.
func TestTwoSessionsDoNotShareState(t *testing.T) {
	alice := newSession(t, "alice", 10)
	bob := newSession(t, "bob", 10)

	alice.RecordView(uuid.New())
	if len(bob.Recent()) != 0 {
		t.Fatalf("bob's recent = %v, want empty", bob.Recent())
	}
}

// TestFlushThenStop walks the teardown a scope runs at eviction, in the
// order it runs it.
func TestFlushThenStop(t *testing.T) {
	s := newSession(t, "alice", 10)
	s.RecordView(uuid.New())

	if err := s.Flush(context.Background()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := s.Recent(); len(got) != 0 {
		t.Fatalf("recent after Stop = %v, want empty", got)
	}
}

// quietLogger is the owned logger type with its output discarded, so a
// test exercises the same code path production does without writing to
// stdout.
func quietLogger() *observability.Logger {
	return &observability.Logger{Logger: slog.New(slog.DiscardHandler)}
}
