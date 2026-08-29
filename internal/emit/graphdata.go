package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/internal/resolve"
)

// graphFunc emits App.Graph() as a compile-time constant: nodes, edges,
// levels, and capabilities as plain data, never indexable back to an
// instance.
func (e *emitter) graphFunc() string {
	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Graph() %s.Graph {\n", e.appType(), e.servoAlias)
	fmt.Fprintf(&b, "\treturn %s.Graph{Nodes: []%s.GraphNode{\n", e.servoAlias, e.servoAlias)
	// Supplied values first, at level 0: the app depends on them before it
	// builds anything, and a graph view that omitted them would show
	// consumers with a dependency on nothing.
	for _, n := range e.resolved.Supplied {
		e.writeGraphNode(&b, n, 0, "")
	}
	for _, n := range e.resolved.Order {
		e.writeGraphNode(&b, n, n.Level, "")
	}
	for _, se := range e.scopes {
		for _, m := range se.Members {
			e.writeGraphNode(&b, m.N, m.N.ScopeLevel, se.S.KeyKey.String())
		}
	}
	b.WriteString("\t}")
	b.WriteString(e.graphScopes())
	b.WriteString("}\n}\n\n")
	return b.String()
}

func (e *emitter) writeGraphNode(b *strings.Builder, n *resolve.Node, level int, scope string) {
	deps := make([]string, len(n.Deps))
	for i, d := range n.Deps {
		deps[i] = d.Key.String()
	}
	// Read before the provider is touched: a supplied value has none.
	binding, pos := "supplied", n.SuppliedPos
	if n.Kind != resolve.NodeSupplied {
		binding, pos = n.Binding, n.Provider.Pos
	}
	fmt.Fprintf(b, "\t\t{Type: %q, Level: %d, Deps: %s, Capabilities: %s, Binding: %q, Pos: %q",
		n.Key.String(), level, stringSliceLiteral(deps), stringSliceLiteral(n.Capabilities), binding, e.posString(pos))
	if scope != "" {
		fmt.Fprintf(b, ", Scope: %q", scope)
	}
	b.WriteString("},\n")
}

// graphScopes emits the Scopes field only when there is one, so an app
// with nothing scoped produces the same literal it always did.
func (e *emitter) graphScopes() string {
	if len(e.scopes) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, ", Scopes: []%s.GraphScope{\n", e.servoAlias)
	for _, se := range e.scopes {
		accessors := make([]string, len(se.Roots))
		for i, re := range se.Roots {
			accessors[i] = re.R.Iface.String()
		}
		members := make([]string, len(se.Members))
		for i, m := range se.Members {
			members[i] = m.N.Key.String()
		}
		borrows := make([]string, len(se.Borrowed))
		for i, n := range se.Borrowed {
			borrows[i] = n.Key.String()
		}
		fmt.Fprintf(&b, "\t\t{Key: %q, Linger: %q, Max: %d, Accessors: %s, Members: %s, Borrows: %s},\n",
			se.S.KeyKey.String(), se.S.Linger.String(), se.S.Max,
			stringSliceLiteral(accessors), stringSliceLiteral(members), stringSliceLiteral(borrows))
	}
	b.WriteString("\t}")
	return b.String()
}

// reportFunc exposes the per-node init timings the constructor recorded,
// with no separate runtime traversal.
func (e *emitter) reportFunc() string {
	return fmt.Sprintf("func (a *%s) Report() %s.StartupReport {\n\treturn a.startupReport\n}\n\n", e.appType(), e.servoAlias)
}
