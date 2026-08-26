package render

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v2/servo"
)

// Text renders g grouped by level so levels stay visually distinguishable
// — a level heading is the simplest way to do that in a plain-text
// terminal.
func Text(g servo.Graph) string {
	levels, grouped := byLevel(g)
	var b strings.Builder
	for _, lvl := range levels {
		fmt.Fprintf(&b, "── Level %d ──\n", lvl)
		for _, n := range grouped[lvl] {
			fmt.Fprintf(&b, "  %s\n", n.Type)
			fmt.Fprintf(&b, "      deps: %s\n", joinOrNone(n.Deps))
			fmt.Fprintf(&b, "      capabilities: %s\n", joinOrNone(n.Capabilities))
			fmt.Fprintf(&b, "      binding: %s\n", n.Binding)
			fmt.Fprintf(&b, "      pos: %s\n", n.Pos)
		}
	}
	return b.String()
}

func joinOrNone(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return strings.Join(ss, ", ")
}
