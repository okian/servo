package render

import (
	"encoding/json"
	"go/ast"
	"go/importer"
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

const fixtureSrc = `
package app

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

type Server struct{}
func NewServer(l *Logger) *Server { return &Server{} }
`

func buildTestResolved(t *testing.T) *resolve.Resolved {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", fixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	candidates, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/app")

	serverPtr := types.NewPointer(pkg.Scope().Lookup("Server").Type())
	spec := &load.Spec{
		Roots: []load.RootDecl{{Key: graph.NewKey(serverPtr, ""), Type: serverPtr, Pos: token.Position{Filename: "spec.go", Line: 5}}},
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec:       spec,
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved
}

func TestToGraphShape(t *testing.T) {
	resolved := buildTestResolved(t)
	g := ToGraph(resolved, nil)
	if len(g.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(g.Nodes))
	}
	server := g.Nodes[1]
	if server.Type != "*example.com/app.Server" || server.Level != 2 {
		t.Errorf("got %+v, want type *example.com/app.Server level 2", server)
	}
	if len(server.Deps) != 1 || server.Deps[0] != "*example.com/app.Logger" {
		t.Errorf("Server.Deps = %v, want [*example.com/app.Logger]", server.Deps)
	}
	// The provider is named by its function, which is what the generated
	// header and App.Graph() carry in place of a position.
	if server.Provider != "app.NewServer" {
		t.Errorf("Server.Provider = %q, want app.NewServer", server.Provider)
	}
	if !strings.HasPrefix(server.Pos, "app.go:") {
		t.Errorf("Server.Pos = %q: `servo graph` keeps the live position", server.Pos)
	}
}

// TestCommittedIsToGraphWithoutPositions: the generated App.Graph() is
// printed from Committed, `servo graph` from ToGraph, and the two may
// differ in positions alone — every other field, nil slices included, is
// the same value.
func TestCommittedIsToGraphWithoutPositions(t *testing.T) {
	resolved := buildTestResolved(t)
	live, committed := ToGraph(resolved, nil), Committed(resolved)
	for i := range live.Nodes {
		if committed.Nodes[i].Pos != "" {
			t.Errorf("Committed node %s carries a position: %q", committed.Nodes[i].Type, committed.Nodes[i].Pos)
		}
		live.Nodes[i].Pos = ""
	}
	a, err := JSON(live)
	if err != nil {
		t.Fatal(err)
	}
	b, err := JSON(committed)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("Committed differs from ToGraph by more than positions:\n%s\n---\n%s", b, a)
	}
}

// TestTextNamesTheProvider: the text format prints the provider beside the
// binding that chose it, with the position after both.
func TestTextNamesTheProvider(t *testing.T) {
	out := Text(ToGraph(buildTestResolved(t), nil))
	want := "      binding: sole candidate\n      provider: app.NewServer\n      pos: app.go:"
	if !strings.Contains(out, want) {
		t.Errorf("text output missing %q:\n%s", want, out)
	}
}

func TestJSONRoundTrips(t *testing.T) {
	g := ToGraph(buildTestResolved(t), nil)
	out, err := JSON(g)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var got servo.Graph
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, out)
	}
	if len(got.Nodes) != len(g.Nodes) {
		t.Errorf("round-tripped %d nodes, want %d", len(got.Nodes), len(g.Nodes))
	}
}

func TestTextGroupsByLevel(t *testing.T) {
	out := Text(ToGraph(buildTestResolved(t), nil))
	if !strings.Contains(out, "Level 1") || !strings.Contains(out, "Level 2") {
		t.Errorf("text output missing level headings:\n%s", out)
	}
	if strings.Index(out, "Level 1") > strings.Index(out, "Level 2") {
		t.Errorf("levels not in ascending order:\n%s", out)
	}
}

func TestDOTContainsNodesAndEdges(t *testing.T) {
	out := DOT(ToGraph(buildTestResolved(t), nil))
	if !strings.HasPrefix(out, "digraph servo {") {
		t.Errorf("DOT output missing digraph header:\n%s", out)
	}
	if !strings.Contains(out, `"*example.com/app.Server" -> "*example.com/app.Logger"`) {
		t.Errorf("DOT output missing the Server->Logger edge:\n%s", out)
	}
}

// TestDOTIncludesCapabilitiesInLabel covers the label += branch for nodes
// that declare capabilities (e.g. Startable, Stoppable) — DOT() only
// appends the capabilities line when the list is non-empty.
func TestDOTIncludesCapabilitiesInLabel(t *testing.T) {
	g := servo.Graph{Nodes: []servo.GraphNode{
		{Type: "*example.com/app.Server", Level: 1, Capabilities: []string{"Startable", "Stoppable"}},
	}}
	out := DOT(g)
	if !strings.Contains(out, "Startable, Stoppable") {
		t.Errorf("DOT output missing capabilities in label:\n%s", out)
	}
	// The line break is DOT's \n escape, written once. A doubled backslash
	// is a literal backslash to dot, which then drew "\n" in the box.
	if !strings.Contains(out, `app.Server\nStartable, Stoppable"`) {
		t.Errorf("DOT label does not break the line with a single \\n escape:\n%s", out)
	}
	if strings.Contains(out, `\\n`) {
		t.Errorf("DOT output carries a doubled \\\\n escape:\n%s", out)
	}
}

// TestMermaidSkipsDanglingDependencyEdge covers the !ok guard in Mermaid's
// edge loop: a Dep naming a type absent from Nodes (never produced by the
// real resolve pipeline, but not an invariant Mermaid itself enforces)
// must be skipped rather than rendered with an empty node id.
func TestMermaidSkipsDanglingDependencyEdge(t *testing.T) {
	g := servo.Graph{Nodes: []servo.GraphNode{
		{Type: "*example.com/app.Server", Level: 1, Deps: []string{"*example.com/app.Missing"}},
	}}
	out := Mermaid(g)
	if strings.Contains(out, "-->") {
		t.Errorf("mermaid output should skip the edge to a missing node, got:\n%s", out)
	}
}

func TestMermaidContainsEdgeAndClass(t *testing.T) {
	out := Mermaid(ToGraph(buildTestResolved(t), nil))
	if !strings.HasPrefix(out, "graph BT") {
		t.Errorf("mermaid output missing graph header:\n%s", out)
	}
	if !strings.Contains(out, "-->") {
		t.Errorf("mermaid output missing an edge:\n%s", out)
	}
	if !strings.Contains(out, "classDef level1") || !strings.Contains(out, "classDef level2") {
		t.Errorf("mermaid output missing per-level classDefs:\n%s", out)
	}
}
