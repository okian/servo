package emit

import (
	"strconv"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/render"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/servo"
)

// TestGraphLiteralPrintsNilAndEmptyApart: JSON tells a nil slice (null)
// from an empty one ([]), so the literal App.Graph() is compiled from has
// to rebuild each exactly, or it would serialize differently from the
// value `servo graph` prints.
func TestGraphLiteralPrintsNilAndEmptyApart(t *testing.T) {
	if got := goStrings(nil); got != "nil" {
		t.Errorf("goStrings(nil) = %s, want nil", got)
	}
	if got := goStrings([]string{}); got != "[]string{}" {
		t.Errorf("goStrings(empty) = %s, want []string{}", got)
	}
	if got := goStrings([]string{`a"b`, "c"}); got != `[]string{"a\"b", "c"}` {
		t.Errorf("goStrings = %s", got)
	}
	e := &emitter{servoAlias: "servo"}
	var b strings.Builder
	e.writeGraphNode(&b, servo.GraphNode{Type: "T", Level: 2, Deps: []string{}, Binding: "traced", Scope: "K", Wraps: "W"})
	if want := "\t\t{Type: \"T\", Level: 2, Deps: []string{}, Capabilities: nil, Binding: \"traced\", Scope: \"K\", Wraps: \"W\"},\n"; b.String() != want {
		t.Errorf("node literal = %q, want %q", b.String(), want)
	}
}

// borrowOrderSrc is the shape the two graph converters used to disagree
// on: Room takes Store before Logger, so the order its borrows are first
// seen in is the reverse of their levels. The generated header always
// listed them by level; `servo graph` listed them as seen.
const borrowOrderSrc = `
package app

import "context"

type RoomKey string

type Logger struct{}

func NewLogger() *Logger { return &Logger{} }

type Store struct{}

func NewStore(l *Logger) *Store { return &Store{} }

type Room struct{}

func NewRoom(k RoomKey, s *Store, l *Logger) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`

// TestEmittedGraphIsCommittedGraph runs the generated App.Graph() and
// compares its JSON with render.Committed's, the value `servo graph`
// prints less its positions: the two views are one value printed twice,
// borrows in level order in both.
func TestEmittedGraphIsCommittedGraph(t *testing.T) {
	resolved, spec := buildResolvedFrom(t, borrowOrderSrc, []string{"Server"}, [][2]string{{"Room", "Rooms"}})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	borrows := render.Committed(resolved).Scopes[0].Borrows
	if strings.Join(borrows, ", ") != "*example.com/app.Logger, *example.com/app.Store" {
		t.Errorf("borrows = %v, want level order: Logger, then Store", borrows)
	}
	assertGraphRunsAsCommitted(t, resolved, out, "app", "app", map[string]string{"app/app.go": borrowOrderSrc})
}

// TestEmittedHTTPGraphIsCommittedGraph is the same check over an HTTP
// plan, whose routes the emitter used to list group by group while
// `servo graph` listed them in the plan's order.
func TestEmittedHTTPGraphIsCommittedGraph(t *testing.T) {
	resolved, spec := buildHTTPResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	assertGraphRunsAsCommitted(t, resolved, out, "httpapp/cmd/app", "main", map[string]string{
		"httpapp/fixture.go":     httpAppSrc,
		"httpapp/cmd/app/app.go": httpMainSrc + "\nfunc main() {}\n",
	})
}

// assertGraphRunsAsCommitted compiles generated into a fixture module and
// has a test there marshal (&App{}).Graph() — a literal, so nothing needs
// constructing — and compare it byte for byte with render.Committed's
// JSON, computed here.
func assertGraphRunsAsCommitted(t *testing.T, resolved *resolve.Resolved, generated []byte, injectorDir, pkgName string, files map[string]string) {
	t.Helper()
	want, err := render.JSON(render.Committed(resolved))
	if err != nil {
		t.Fatal(err)
	}
	files[injectorDir+"/graph_test.go"] = `package ` + pkgName + `

import (
	"encoding/json"
	"testing"
)

func TestGraph(t *testing.T) {
	got, err := json.MarshalIndent((&App{}).Graph(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := ` + strconv.Quote(want) + `; string(got)+"\n" != want {
		t.Fatalf("App.Graph() serializes as\n%s\nbut servo graph's value is\n%s", got, want)
	}
}
`
	runGenerated(t, injectorDir, generated, files)
}
