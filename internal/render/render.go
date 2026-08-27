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
	nodes := make([]servo.GraphNode, len(resolved.Order))
	for i, n := range resolved.Order {
		deps := make([]string, len(n.Deps))
		for j, d := range n.Deps {
			deps[j] = d.Key.String()
		}
		nodes[i] = servo.GraphNode{
			Type:         n.Key.String(),
			Level:        n.Level,
			Deps:         deps,
			Capabilities: append([]string(nil), n.Capabilities...),
			Binding:      n.Binding,
			Pos:          n.Provider.Pos.String(),
		}
	}
	return servo.Graph{Nodes: nodes}
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
