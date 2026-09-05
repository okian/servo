package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/internal/route"
	"github.com/okian/servo/v3/servo"
)

// httpResolved hand-builds the minimal Resolved an HTTP plan produces: two
// singletons and a plan with one default-group route (one dep, one
// extracted arg), a group, one middleware attachment and one extractor.
func httpResolved() *resolve.Resolved {
	repo := &resolve.Node{Key: graph.Key{Type: "*app.Repo"}, Level: 1, Provider: &graph.Provider{}}
	guard := &resolve.Node{Key: graph.Key{Type: "*mw.Guard"}, Level: 1, Provider: &graph.Provider{}}
	ex := &resolve.HTTPExtractor{
		Node:     &resolve.Node{Key: graph.Key{Type: "*mw.UserExtractor"}, Level: 1, Provider: &graph.Provider{}},
		Produces: graph.Key{Type: "*mw.User"},
	}
	return &resolve.Resolved{
		Order: []*resolve.Node{repo},
		HTTP: &resolve.HTTPPlan{
			Groups: []string{"telemetry"},
			Routes: []*resolve.HTTPRoute{{
				Route: &route.Route{Method: "POST", Pattern: "/order/{id}", Group: "", Name: "app.Order"},
				Args:  []resolve.HTTPArg{{Node: repo}, {Extractor: ex}},
			}},
			Uses: []*resolve.HTTPUse{{
				Node: guard,
			}},
			Extractors: []*resolve.HTTPExtractor{ex},
		},
	}
}

func TestToGraphAttributesHTTP(t *testing.T) {
	g := ToGraph(httpResolved())
	if g.HTTP == nil {
		t.Fatalf("Graph.HTTP is nil")
	}
	if len(g.HTTP.Groups) != 1 || g.HTTP.Groups[0] != "telemetry" {
		t.Errorf("groups = %v", g.HTTP.Groups)
	}
	if len(g.HTTP.Routes) != 1 {
		t.Fatalf("routes = %+v", g.HTTP.Routes)
	}
	rt := g.HTTP.Routes[0]
	if rt.Method != "POST" || rt.Pattern != "/order/{id}" || rt.Group != "" || rt.Handler != "app.Order" {
		t.Errorf("route = %+v", rt)
	}
	if len(rt.Args) != 2 || rt.Args[0] != "*app.Repo" || rt.Args[1] != "*mw.User (extracted)" {
		t.Errorf("args = %v", rt.Args)
	}
	if len(g.HTTP.Uses) != 1 || g.HTTP.Uses[0].Type != "*mw.Guard" || g.HTTP.Uses[0].Scope != "server" {
		t.Errorf("uses = %+v", g.HTTP.Uses)
	}
	if len(g.HTTP.Extractors) != 1 || g.HTTP.Extractors[0].Produces != "*mw.User" {
		t.Errorf("extractors = %+v", g.HTTP.Extractors)
	}
}

// The JSON schema gains an "http" object; a plan-less graph omits it, so
// existing consumers see byte-identical output.
func TestGraphJSONIncludesHTTP(t *testing.T) {
	b, err := json.Marshal(ToGraph(httpResolved()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"http":`, `"routes":`, `"handler":"app.Order"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s: %s", want, b)
		}
	}

	plain, err := json.Marshal(servo.Graph{Nodes: []servo.GraphNode{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "http") {
		t.Errorf("plan-less graph must omit the http field: %s", plain)
	}
}
