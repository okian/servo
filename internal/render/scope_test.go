package render

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/servo"
)

const scopedFixtureSrc = `
package app

import "context"

type RoomKey string

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type RoomLog struct{}
func NewRoomLog(k RoomKey, l *Logger) *RoomLog { return &RoomLog{} }

type Room struct{}
func NewRoom(k RoomKey, rl *RoomLog) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Server struct{}
func NewServer(r Rooms, l *Logger) *Server { return &Server{} }
`

// buildScopedResolved runs the real resolver, so ToGraph is exercised
// against a plan the pipeline actually produces rather than one written by
// hand — which is the only way graphScope and borrowedOf get to run at
// all.
func buildScopedResolved(t *testing.T) *resolve.Resolved {
	t.Helper()
	ctxPkg := loadContextPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", scopedFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: singlePkgImporter{ctxPkg.Types}}
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

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec: &load.Spec{
			Roots: []load.RootDecl{{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 5}}},
			Scopes: []load.ScopeDecl{{
				Impl: graph.NewKey(ptr("Room"), ""), ImplType: ptr("Room"),
				Iface: graph.NewKey(lookup("Rooms"), ""), IfaceType: lookup("Rooms"),
				Pos: token.Position{Filename: "spec.go", Line: 6},
			}},
		},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Fset:       fset,
		Pkgs:       []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved
}

func loadContextPackage(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, "context")
	if err != nil || len(pkgs) != 1 {
		t.Fatalf("load context: %v", err)
	}
	return pkgs[0]
}

type singlePkgImporter struct{ pkg *types.Package }

func (i singlePkgImporter) Import(path string) (*types.Package, error) {
	if path == "context" {
		return i.pkg, nil
	}
	return nil, &notFoundError{path}
}

type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return "no package " + e.path }

// TestToGraphAttributesScopes is the real conversion: members carry their
// scope's key and their level within it, and the scope itself reports what
// it holds and what it borrows.
func TestToGraphAttributesScopes(t *testing.T) {
	g := ToGraph(buildScopedResolved(t))

	if len(g.Scopes) != 1 {
		t.Fatalf("got %d scopes, want 1", len(g.Scopes))
	}
	s := g.Scopes[0]
	if s.Key != "example.com/app.RoomKey" {
		t.Fatalf("scope key = %s", s.Key)
	}
	if s.Linger != "30s" || s.Max != 10000 {
		t.Fatalf("scope policy = linger %s max %d, want the declared defaults", s.Linger, s.Max)
	}
	if strings.Join(s.Accessors, ",") != "example.com/app.Rooms" {
		t.Fatalf("accessors = %v", s.Accessors)
	}
	if strings.Join(s.Members, ",") != "*example.com/app.RoomLog,*example.com/app.Room" {
		t.Fatalf("members = %v", s.Members)
	}
	if strings.Join(s.Borrows, ",") != "*example.com/app.Logger" {
		t.Fatalf("borrows = %v — the logger is shared, not one per room", s.Borrows)
	}

	byType := map[string]servo.GraphNode{}
	for _, n := range g.Nodes {
		byType[n.Type] = n
	}
	if got := byType["*example.com/app.Logger"].Scope; got != "" {
		t.Fatalf("the logger is attributed to scope %q", got)
	}
	// Levels within the scope start at its own floor, not the app's.
	if got := byType["*example.com/app.RoomLog"]; got.Scope != s.Key || got.Level != 1 {
		t.Fatalf("RoomLog = %+v, want scope %s at scope level 1", got, s.Key)
	}
	if got := byType["*example.com/app.Room"]; got.Level != 2 {
		t.Fatalf("Room level = %d, want 2", got.Level)
	}
}

func TestRenderersAcceptARealScopedGraph(t *testing.T) {
	g := ToGraph(buildScopedResolved(t))
	for name, out := range map[string]string{"text": Text(g), "dot": DOT(g), "mermaid": Mermaid(g)} {
		if !strings.Contains(out, "example.com/app.RoomKey") {
			t.Errorf("%s output does not mention the scope key:\n%s", name, out)
		}
	}
	if _, err := JSON(g); err != nil {
		t.Fatalf("JSON: %v", err)
	}
}

// scopedGraph is what ToGraph produces for one scope: two singletons, two
// members, and a consumer whose only edge into the scope is the accessor
// interface — which is not a node.
func scopedGraph() servo.Graph {
	return servo.Graph{
		Nodes: []servo.GraphNode{
			{Type: "*app.Logger", Level: 1, Capabilities: []string{"Finalizer"}, Binding: "sole candidate", Pos: "logger.go:1"},
			{Type: "*app.Server", Level: 2, Deps: []string{"app.Rooms", "*app.Logger"}, Binding: "sole candidate", Pos: "api.go:1"},
			{Type: "*app.RoomLog", Level: 1, Deps: []string{"app.RoomKey"}, Binding: "sole candidate", Pos: "chat.go:1", Scope: "app.RoomKey"},
			{Type: "*app.Room", Level: 2, Deps: []string{"app.RoomKey", "*app.RoomLog"}, Binding: "sole candidate", Pos: "chat.go:2", Scope: "app.RoomKey"},
		},
		Scopes: []servo.GraphScope{{
			Key: "app.RoomKey", Linger: "30s", Max: 10000,
			Accessors: []string{"app.Rooms"},
			Members:   []string{"*app.RoomLog", "*app.Room"},
			Borrows:   []string{"*app.Logger"},
		}},
	}
}

func TestTextSeparatesScopeLevels(t *testing.T) {
	out := Text(scopedGraph())
	for _, want := range []string{
		"── Level 1 ──",
		"── Level 2 ──",
		"══ app.RoomKey ══",
		"linger: 30s   max: 10000",
		"accessors: app.Rooms",
		"borrows:   *app.Logger",
		"── Scope level 1 ──",
		"── Scope level 2 ──",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
	// A scoped node must not appear under the app's own level headings.
	appSection := out[:strings.Index(out, "══ app.RoomKey ══")]
	if strings.Contains(appSection, "*app.Room") {
		t.Errorf("a scoped node appeared among the app's levels:\n%s", appSection)
	}
}

// TestJSONOmitsScopeFieldsWhenUnscoped is the additive-schema claim: a
// graph with nothing scoped serializes exactly as it did before scopes
// existed.
func TestJSONOmitsScopeFieldsWhenUnscoped(t *testing.T) {
	g := servo.Graph{Nodes: []servo.GraphNode{{Type: "*app.Logger", Level: 1, Binding: "sole candidate", Pos: "logger.go:1"}}}
	out, err := JSON(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "scope") {
		t.Errorf("unscoped graph mentions a scope:\n%s", out)
	}
}

func TestJSONCarriesScopeAttribution(t *testing.T) {
	out, err := JSON(scopedGraph())
	if err != nil {
		t.Fatal(err)
	}
	var round servo.Graph
	if err := json.Unmarshal([]byte(out), &round); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if len(round.Scopes) != 1 || round.Scopes[0].Key != "app.RoomKey" || round.Scopes[0].Max != 10000 {
		t.Fatalf("scopes did not round-trip: %+v", round.Scopes)
	}
	var scoped int
	for _, n := range round.Nodes {
		if n.Scope != "" {
			scoped++
		}
	}
	if scoped != 2 {
		t.Fatalf("got %d scoped nodes after round-trip, want 2", scoped)
	}
}

// TestAccessorEdgesPointAtTheKey covers the one edge that would otherwise
// dangle: a consumer depends on the accessor interface, which is generated
// code rather than a resolved node.
func TestAccessorEdgesPointAtTheKey(t *testing.T) {
	dot := DOT(scopedGraph())
	if !strings.Contains(dot, `"*app.Server" -> "app.RoomKey"`) {
		t.Errorf("DOT does not route the accessor edge to the scope key:\n%s", dot)
	}
	if strings.Contains(dot, `-> "app.Rooms"`) {
		t.Errorf("DOT left a dangling edge to the accessor interface:\n%s", dot)
	}
	if !strings.Contains(dot, "subgraph cluster_scope0 {") {
		t.Errorf("DOT does not cluster the scope:\n%s", dot)
	}

	mmd := Mermaid(scopedGraph())
	if !strings.Contains(mmd, "subgraph scope0[") {
		t.Errorf("Mermaid does not group the scope:\n%s", mmd)
	}
	if !strings.Contains(mmd, "k0[\"app.RoomKey\"]:::scopekey") {
		t.Errorf("Mermaid does not render the scope key:\n%s", mmd)
	}
	// n1 is *app.Server; k0 is the scope key.
	if !strings.Contains(mmd, "n1 --> k0") {
		t.Errorf("Mermaid does not route the accessor edge to the scope key:\n%s", mmd)
	}
}

// TestRenderersUnchangedWithoutScopes pins the no-scope path: the DOT and
// Mermaid output for a plain graph carries none of the scope decoration.
func TestRenderersUnchangedWithoutScopes(t *testing.T) {
	g := servo.Graph{Nodes: []servo.GraphNode{
		{Type: "*app.Logger", Level: 1, Binding: "sole candidate", Pos: "logger.go:1"},
		{Type: "*app.Server", Level: 2, Deps: []string{"*app.Logger"}, Binding: "sole candidate", Pos: "api.go:1"},
	}}
	for name, out := range map[string]string{"dot": DOT(g), "mermaid": Mermaid(g), "text": Text(g)} {
		for _, unwanted := range []string{"subgraph", "scopekey", "Scope level", "══"} {
			if strings.Contains(out, unwanted) {
				t.Errorf("%s output mentions %q for a graph with no scopes:\n%s", name, unwanted, out)
			}
		}
	}
}
