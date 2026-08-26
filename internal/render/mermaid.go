package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v2/servo"
)

// Mermaid renders g as a Mermaid flowchart, colored by level.
func Mermaid(g servo.Graph) string {
	var b strings.Builder
	b.WriteString("graph BT\n")

	ids := make(map[string]string, len(g.Nodes))
	for i, n := range g.Nodes {
		ids[n.Type] = fmt.Sprintf("n%d", i)
	}
	for _, n := range g.Nodes {
		label := strings.ReplaceAll(n.Type, `"`, `'`)
		fmt.Fprintf(&b, "  %s[%q]:::level%d\n", ids[n.Type], label, n.Level)
	}
	for _, n := range g.Nodes {
		for _, dep := range n.Deps {
			depID, ok := ids[dep]
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
	return b.String()
}
