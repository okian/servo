package render

import (
	"go/token"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// tracedResolved is the smallest graph with a servo.Trace wrapper in it:
// the implementation, the tracer provider, the wrapper over both, and
// one consumer. Built by hand, since the converter reads only keys,
// kinds and positions.
func tracedResolved() *resolve.Resolved {
	provider := func(name, pos string) *resolve.Node {
		return &resolve.Node{
			Key: graph.Key{Type: name}, Kind: resolve.NodeProvider, Binding: "sole candidate", Level: 1,
			Provider: &graph.Provider{Pos: token.Position{Filename: pos, Line: 3}},
		}
	}
	db := provider("*example.com/app.DB", "db.go")
	sdk := provider("*example.com/app.SDK", "sdk.go")
	wrapper := &resolve.Node{
		Key: graph.Key{Type: "example.com/app.Store", Tag: resolve.TracedTag}, Kind: resolve.NodeTraced, Binding: "traced", Level: 2,
		Deps:   []*resolve.Node{db, sdk},
		Traced: &resolve.Traced{Decl: &load.TraceDecl{Pos: token.Position{Filename: "spec.go", Line: 12}}, Inner: db, Tracer: sdk},
	}
	server := provider("*example.com/app.Server", "server.go")
	server.Level, server.Deps = 3, []*resolve.Node{wrapper}
	return &resolve.Resolved{Order: []*resolve.Node{db, sdk, wrapper, server}, Roots: []*resolve.Node{server}}
}

func TestToGraphReportsAWrapperWithWhatItWraps(t *testing.T) {
	g := ToGraph(tracedResolved(), nil)
	var found bool
	for _, n := range g.Nodes {
		if n.Type != "example.com/app.Store#traced" {
			if n.Wraps != "" {
				t.Errorf("%s carries Wraps=%q, want it only on the wrapper", n.Type, n.Wraps)
			}
			continue
		}
		found = true
		if n.Binding != "traced" || n.Wraps != "*example.com/app.DB" || n.Pos != "spec.go:12" || n.Level != 2 {
			t.Errorf("wrapper node = %+v", n)
		}
		if strings.Join(n.Deps, ",") != "*example.com/app.DB,*example.com/app.SDK" {
			t.Errorf("wrapper deps = %v", n.Deps)
		}
	}
	if !found {
		t.Fatalf("no wrapper node in %+v", g.Nodes)
	}
}

// TestJSONCarriesWrapsAndOmitsItElsewhere: the field is omitempty, so a
// graph without a wrapper serialises as it always did.
func TestJSONCarriesWrapsAndOmitsItElsewhere(t *testing.T) {
	out, err := JSON(ToGraph(tracedResolved(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, `"wraps"`) != 1 || !strings.Contains(out, `"wraps": "*example.com/app.DB"`) {
		t.Fatalf("expected exactly one wraps field, naming the implementation:\n%s", out)
	}
	for _, format := range []func(string) string{
		func(_ string) string { return Text(ToGraph(tracedResolved(), nil)) },
		func(_ string) string { return DOT(ToGraph(tracedResolved(), nil)) },
		func(_ string) string { return Mermaid(ToGraph(tracedResolved(), nil)) },
	} {
		if rendered := format(""); !strings.Contains(rendered, "example.com/app.Store#traced") {
			t.Errorf("a renderer dropped the wrapper node:\n%s", rendered)
		}
	}
}
