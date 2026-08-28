package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/servo"
)

// Mermaid renders g as a Mermaid flowchart, colored by level.
func Mermaid(g servo.Graph) string {
	var b strings.Builder
	b.WriteString("graph BT\n")

	ids := make(map[string]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[n.Type] = fmt.Sprintf("n%d", i)
	}
	for i, s := range g.Scopes {
		ids[s.Key] = fmt.Sprintf("k%d", i)
	}

	for _, n := range singletonsOf(g).Nodes {
		writeMermaidNode(&b, ids, n)
	}
	// A subgraph per scope: the members of one entry, plus the key itself
	// as the thing that decides which entry a caller lands in.
	for i, s := range g.Scopes {
		fmt.Fprintf(&b, "  subgraph scope%d[\"scope %s — linger %s, max %d\"]\n", i, strings.ReplaceAll(s.Key, `"`, `'`), s.Linger, s.Max)
		fmt.Fprintf(&b, "    %s[%q]:::scopekey\n", ids[s.Key], strings.ReplaceAll(s.Key, `"`, `'`))
		for _, n := range membersOf(g, s.Key).Nodes {
			b.WriteString("  ")
			writeMermaidNode(&b, ids, n)
		}
		b.WriteString("  end\n")
	}
	alias := accessorAlias(g)
	for _, n := range g.Nodes {
		for _, dep := range n.Deps {
			depID, ok := ids[resolveDep(alias, dep)]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "  %s --> %s\n", ids[n.Type], depID)
		}
	}

	levels, _ := byLevel(g)
	for _, lvl := range levels {
		fmt.Fprintf(&b, "  classDef level%d fill:%s;\n", lvl, colorFor(lvl))
	}
	if len(g.Scopes) > 0 {
		b.WriteString("  classDef scopekey fill:#fef9c3,stroke-dasharray: 4 2;\n")
	}
	return b.String()
}

func writeMermaidNode(b *strings.Builder, ids map[string]string, n servo.GraphNode) {
	label := strings.ReplaceAll(n.Type, `"`, `'`)
	fmt.Fprintf(b, "  %s[%q]:::level%d\n", ids[n.Type], label, n.Level)
}
