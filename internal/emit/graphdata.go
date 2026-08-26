package emit

import (
	"fmt"
	"strings"
)

// graphFunc emits App.Graph() as a compile-time constant: nodes, edges,
// levels, and capabilities as plain data, never indexable back to an
// instance.
func (e *emitter) graphFunc() string {
	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Graph() %s.Graph {\n", e.appType(), e.servoAlias)
	fmt.Fprintf(&b, "\treturn %s.Graph{Nodes: []%s.GraphNode{\n", e.servoAlias, e.servoAlias)
	for _, n := range e.resolved.Order {
		deps := make([]string, len(n.Deps))
		for i, d := range n.Deps {
			deps[i] = d.Key.String()
		}
		fmt.Fprintf(&b, "\t\t{Type: %q, Level: %d, Deps: %s, Capabilities: %s, Binding: %q, Pos: %q},\n",
			n.Key.String(), n.Level, stringSliceLiteral(deps), stringSliceLiteral(n.Capabilities), n.Binding, e.posString(n.Provider.Pos))
	}
	b.WriteString("\t}}\n}\n\n")
	return b.String()
}

// reportFunc exposes the per-node init timings the constructor recorded,
// with no separate runtime traversal.
func (e *emitter) reportFunc() string {
	return fmt.Sprintf("func (a *%s) Report() %s.StartupReport {\n\treturn a.startupReport\n}\n\n", e.appType(), e.servoAlias)
}
