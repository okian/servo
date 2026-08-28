package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/servo"
)

// Text renders g grouped by level so levels stay visually distinguishable
// — a level heading is the simplest way to do that in a plain-text
// terminal.
func Text(g servo.Graph) string {
	var b strings.Builder
	writeLevels(&b, singletonsOf(g), "Level")

	// Each scope gets its own level headings, because a scoped node's
	// level is counted within its scope and would be meaningless
	// interleaved with the app's.
	for _, s := range g.Scopes {
		fmt.Fprintf(&b, "\n══ %s ══\n", s.Key)
		fmt.Fprintf(&b, "  linger: %s   max: %d\n", s.Linger, s.Max)
		fmt.Fprintf(&b, "  accessors: %s\n", joinOrNone(s.Accessors))
		fmt.Fprintf(&b, "  borrows:   %s\n", joinOrNone(s.Borrows))
		writeLevels(&b, membersOf(g, s.Key), "Scope level")
	}
	return b.String()
}

func writeLevels(b *strings.Builder, g servo.Graph, label string) {
	levels, grouped := byLevel(g)
	for _, lvl := range levels {
		fmt.Fprintf(b, "── %s %d ──\n", label, lvl)
		for _, n := range grouped[lvl] {
			fmt.Fprintf(b, "  %s\n", n.Type)
			fmt.Fprintf(b, "      deps: %s\n", joinOrNone(n.Deps))
			fmt.Fprintf(b, "      capabilities: %s\n", joinOrNone(n.Capabilities))
			fmt.Fprintf(b, "      binding: %s\n", n.Binding)
			fmt.Fprintf(b, "      pos: %s\n", n.Pos)
		}
	}
}

func singletonsOf(g servo.Graph) servo.Graph {
	var out servo.Graph
	for _, n := range g.Nodes {
		if n.Scope == "" {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out
}

func membersOf(g servo.Graph, key string) servo.Graph {
	var out servo.Graph
	for _, n := range g.Nodes {
		if n.Scope == key {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}
