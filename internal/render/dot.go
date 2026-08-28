package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/servo"
)

// DOT renders g for Graphviz, coloring nodes by level so levels stay
// visually distinguishable, and pointing edges from dependent to
// dependency.
func DOT(g servo.Graph) string {
	var b strings.Builder
	b.WriteString("digraph servo {\n")
	b.WriteString("  rankdir=BT;\n")
	b.WriteString("  node [shape=box, style=filled, fontname=\"monospace\"];\n\n")

	writeDotNodes(&b, singletonsOf(g), "")
	// One cluster per scope, so the boundary between "one per process" and
	// "one per key" is visible rather than inferred from edge directions.
	for i, s := range g.Scopes {
		fmt.Fprintf(&b, "  subgraph cluster_scope%d {\n", i)
		fmt.Fprintf(&b, "    label=%q;\n    style=dashed;\n    color=\"#64748b\";\n", fmt.Sprintf("scope %s  (linger %s, max %d)", s.Key, s.Linger, s.Max))
		writeDotNodes(&b, membersOf(g, s.Key), "  ")
		fmt.Fprintf(&b, "    %q [label=%q, shape=note, fillcolor=\"#fef9c3\"];\n", s.Key, s.Key+"\\n(scope key)")
		b.WriteString("  }\n")
	}
	b.WriteString("\n")
	alias := accessorAlias(g)
	for _, n := range g.Nodes {
		for _, dep := range n.Deps {
			fmt.Fprintf(&b, "  %q -> %q;\n", n.Type, resolveDep(alias, dep))
		}
	}
	b.WriteString("}\n")
	return b.String()
}

func writeDotNodes(b *strings.Builder, g servo.Graph, indent string) {
	for _, n := range g.Nodes {
		label := n.Type
		if len(n.Capabilities) > 0 {
			label += "\\n" + strings.Join(n.Capabilities, ", ")
		}
		fmt.Fprintf(b, "  %s%q [label=%q, fillcolor=%q];\n", indent, n.Type, label, colorFor(n.Level))
	}
}
