// Package render turns a resolved graph into the human/machine formats
// `servo graph` exports: text, JSON, DOT, and Mermaid.
package render

import (
	"sort"

	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/servo"
)

// ToGraph converts a resolved graph into the same servo.Graph shape the
// generated App.Graph() method returns, so build-time and runtime views
// share one schema — not by special-casing JSON, but because both paths
// populate the identical struct.
func ToGraph(resolved *resolve.Resolved) servo.Graph {
	nodes := make([]servo.GraphNode, 0, len(resolved.Order))
	for _, n := range resolved.Order {
		nodes = append(nodes, graphNode(n, n.Level, ""))
	}
	// Scoped members come after the singletons, each carrying its scope's
	// key and its level within that scope rather than within the app.
	var scopes []servo.GraphScope
	for _, s := range resolved.Scopes {
		for _, n := range s.Order {
			nodes = append(nodes, graphNode(n, n.ScopeLevel, s.KeyKey.String()))
		}
		scopes = append(scopes, graphScope(s))
	}
	return servo.Graph{Nodes: nodes, Scopes: scopes}
}

func graphNode(n *resolve.Node, level int, scope string) servo.GraphNode {
	deps := make([]string, len(n.Deps))
	for j, d := range n.Deps {
		deps[j] = d.Key.String()
	}
	return servo.GraphNode{
		Type:         n.Key.String(),
		Level:        level,
		Deps:         deps,
		Capabilities: append([]string(nil), n.Capabilities...),
		Binding:      n.Binding,
		Pos:          n.Provider.Pos.String(),
		Scope:        scope,
	}
}

func graphScope(s *resolve.Scope) servo.GraphScope {
	accessors := make([]string, len(s.Roots))
	for i, root := range s.Roots {
		accessors[i] = root.Iface.String()
	}
	members := make([]string, len(s.Order))
	for i, n := range s.Order {
		members[i] = n.Key.String()
	}
	borrows := borrowedOf(s)
	return servo.GraphScope{
		Key: s.KeyKey.String(), Linger: s.Linger.String(), Max: s.Max,
		Accessors: accessors, Members: members, Borrows: borrows,
	}
}

// borrowedOf lists the singletons a scope's members and extractors depend
// on. They are constructed once by the App and shared by every instance,
// so they are reported separately from the members rather than counted as
// part of what an instance holds.
func borrowedOf(s *resolve.Scope) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n *resolve.Node) {
		if n == nil || n.Kind != resolve.NodeProvider || n.Scope != nil || seen[n.Key.String()] {
			return
		}
		seen[n.Key.String()] = true
		out = append(out, n.Key.String())
	}
	for _, n := range s.Order {
		for _, d := range n.Deps {
			add(d)
		}
	}
	for _, root := range s.Roots {
		for _, d := range root.ExtractorDeps {
			add(d)
		}
	}
	return out
}

// byLevel groups g's nodes by level, ascending, each level's nodes kept in
// their original relative order.
func byLevel(g servo.Graph) (levels []int, grouped map[int][]servo.GraphNode) {
	grouped = map[int][]servo.GraphNode{}
	for _, n := range g.Nodes {
		if _, ok := grouped[n.Level]; !ok {
			levels = append(levels, n.Level)
		}
		grouped[n.Level] = append(grouped[n.Level], n)
	}
	sort.Ints(levels)
	return levels, grouped
}

var levelPalette = []string{"#dbeafe", "#bfdbfe", "#93c5fd", "#60a5fa", "#3b82f6", "#2563eb", "#1d4ed8"}

func colorFor(level int) string {
	return levelPalette[level%len(levelPalette)]
}

// accessorAlias maps each scope's accessor interfaces onto that scope's
// key. An accessor is not a node — it is generated code, not a resolved
// provider — so a consumer's edge to one would otherwise dangle. Pointing
// it at the key instead draws the edge that is actually true: this
// singleton reaches instances chosen by that key.
func accessorAlias(g servo.Graph) map[string]string {
	alias := map[string]string{}
	for _, s := range g.Scopes {
		for _, a := range s.Accessors {
			alias[a] = s.Key
		}
	}
	return alias
}

func resolveDep(alias map[string]string, dep string) string {
	if target, ok := alias[dep]; ok {
		return target
	}
	return dep
}
