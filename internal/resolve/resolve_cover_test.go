package resolve

import (
	"go/token"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

const extractorCtxSrc = `package app

import "context"

type RoomKey string

type Room struct{}

func NewRoom(k RoomKey) *Room { return &Room{} }

func (*Room) ScopeKey(ctx context.Context, again context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`

// TestExtractorCannotTakeTheConstructionContext: a ScopeKey extractor's
// parameters after the first are singletons resolved once, so a second
// context.Context there would be the ctx New was called with, long gone
// by the time the extractor runs. It is refused at the extractor, the
// line to change, and tells the reader to use the first parameter.
func TestExtractorCannotTakeTheConstructionContext(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", extractorCtxSrc})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.Scopes = []load.ScopeDecl{{
		Impl: graph.NewKey(m.ptr("example.com/mod/app", "Room"), ""), ImplType: m.ptr("example.com/mod/app", "Room"),
		Iface: graph.NewKey(m.typ("example.com/mod/app", "Rooms"), ""), IfaceType: m.typ("example.com/mod/app", "Rooms"),
		Pos: token.Position{Filename: "/mod/spec.go", Line: 11},
	}}

	d := onlyDiagnostic(t, in)
	for _, want := range []string{
		"servo: *example.com/mod/app.Room's ScopeKey extractor takes a second context.Context\n",
		"Its first parameter is the context the instance is acquired with.",
		"Read what you need from the first one.\n",
	} {
		if !strings.Contains(d.Message, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, d.Message)
		}
	}
	wantPos := token.Position{Filename: "/mod/app/x.go", Line: lineContaining(t, extractorCtxSrc, ") ScopeKey("), Column: 14}
	if d.Pos.String() != wantPos.String() {
		t.Errorf("diagnostic at %v, want the extractor at %v", d.Pos, wantPos)
	}
}

const promotedNotesSrc = `package app

import "example.org/base"

// Worker gets both methods from a dependency's type, so neither has a
// position in the main module and both notes land on the constructor.
type Worker struct{ base.Base }

func NewWorker() *Worker { return &Worker{} }
`

// TestNotesAtOnePositionAreOrderedByMessage: notes are sorted by position,
// and two can share one — a method promoted from a dependency's type is
// reported at the constructor. The message breaks the tie, so two runs
// over one module print the same notes in the same order, whatever order
// they were found in.
func TestNotesAtOnePositionAreOrderedByMessage(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.org/base", "package base\n\ntype Base struct{}\n\nfunc (Base) Run() error   { return nil }\nfunc (Base) Close() error { return nil }\n"},
		pkgSrc{"example.com/mod/app", promotedNotesSrc})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Worker")))

	if len(r.Notes) != 2 {
		t.Fatalf("got %d notes, want 2:\n%s", len(r.Notes), diagText(r.Notes))
	}
	at := token.Position{Filename: "/mod/app/x.go", Line: lineContaining(t, promotedNotesSrc, "func NewWorker"), Column: 6}
	for _, n := range r.Notes {
		if n.Pos.String() != at.String() {
			t.Errorf("note at %v, want the constructor at %v: %s", n.Pos, at, n.Message)
		}
	}
	// The Run note is found first; "Worker has" sorts before "Worker's".
	if !strings.HasPrefix(r.Notes[0].Message, "servo: note: *example.com/mod/app.Worker has a Close method but no Stop(ctx context.Context) error") {
		t.Errorf("first note = %q, want the Close note", r.Notes[0].Message)
	}
	if !strings.HasPrefix(r.Notes[1].Message, "servo: note: *example.com/mod/app.Worker's Run method has the signature Run() error, not Run(ctx context.Context) error") {
		t.Errorf("second note = %q, want the Run note", r.Notes[1].Message)
	}
}
