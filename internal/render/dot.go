package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v2/servo"
)

// DOT renders g for Graphviz, coloring nodes by level so levels stay
// visually distinguishable, and pointing edges from dependent to
// dependency.
func DOT(g servo.Graph) string {
	var b strings.Builder
	b.WriteString("digraph servo {\n")
	b.WriteString("  rankdir=BT;\n")
	b.WriteString("  node [shape=box, style=filled, fontname=\"monospace\"];\n\n")

	for _, n := range g.Nodes {
		label := n.Type
		if len(n.Capabilities) > 0 {
			label += "\\n" + strings.Join(n.Capabilities, ", ")
		}
		fmt.Fprintf(&b, "  %q [label=%q, fillcolor=%q];\n", n.Type, label, colorFor(n.Level))
	}
	b.WriteString("\n")
	for _, n := range g.Nodes {
		for _, dep := range n.Deps {
			fmt.Fprintf(&b, "  %q -> %q;\n", n.Type, dep)
		}
	}
	b.WriteString("}\n")
	return b.String()
}
