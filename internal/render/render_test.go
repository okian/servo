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
	g := ToGraph(resolved)
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
}

func TestJSONRoundTrips(t *testing.T) {
	g := ToGraph(buildTestResolved(t))
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
	out := Text(ToGraph(buildTestResolved(t)))
	if !strings.Contains(out, "Level 1") || !strings.Contains(out, "Level 2") {
		t.Errorf("text output missing level headings:\n%s", out)
	}
	if strings.Index(out, "Level 1") > strings.Index(out, "Level 2") {
		t.Errorf("levels not in ascending order:\n%s", out)
	}
}

func TestDOTContainsNodesAndEdges(t *testing.T) {
	out := DOT(ToGraph(buildTestResolved(t)))
	if !strings.HasPrefix(out, "digraph servo {") {
		t.Errorf("DOT output missing digraph header:\n%s", out)
	}
	if !strings.Contains(out, `"*example.com/app.Server" -> "*example.com/app.Logger"`) {
		t.Errorf("DOT output missing the Server->Logger edge:\n%s", out)
	}
}

func TestMermaidContainsEdgeAndClass(t *testing.T) {
	out := Mermaid(ToGraph(buildTestResolved(t)))
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
