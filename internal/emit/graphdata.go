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
	b.WriteString(e.graphHTTP())
	b.WriteString("}\n}\n\n")
	return b.String()
}

// graphHTTP emits the HTTP field only when the spec declares servo.HTTP(),
// so a plan-less app produces the same literal it always did. The rendering
// mirrors internal/render's converter — `servo graph` at build time and
// App.Graph() at run time must agree.
func (e *emitter) graphHTTP() string {
	if e.http == nil {
		return ""
	}
	plan := e.http.Plan
	var b strings.Builder
	fmt.Fprintf(&b, ", HTTP: &%s.GraphHTTP{\n", e.servoAlias)
	if len(plan.Groups) > 0 {
		fmt.Fprintf(&b, "\t\tGroups: %s,\n", stringSliceLiteral(plan.Groups))
	}
	fmt.Fprintf(&b, "\t\tRoutes: []%s.GraphRoute{\n", e.servoAlias)
	for _, g := range e.http.Groups {
		for _, re := range g.Routes {
			rt := re.R.Route
			fmt.Fprintf(&b, "\t\t\t{Method: %q, Pattern: %q", rt.Method, rt.Pattern)
			if rt.Group != "" {
				fmt.Fprintf(&b, ", Group: %q", rt.Group)
			}
			fmt.Fprintf(&b, ", Handler: %q", rt.Name)
			if args := resolve.HTTPRouteArgs(re.R); len(args) > 0 {
				fmt.Fprintf(&b, ", Args: %s", stringSliceLiteral(args))
			}
			fmt.Fprintf(&b, ", Pos: %q},\n", e.posString(rt.Pos))
		}
	}
	b.WriteString("\t\t},\n")
	if len(plan.Uses) > 0 {
		fmt.Fprintf(&b, "\t\tUses: []%s.GraphUse{\n", e.servoAlias)
		for _, u := range plan.Uses {
			fmt.Fprintf(&b, "\t\t\t{Type: %q, Scope: %q},\n", u.Node.Key.String(), resolve.HTTPUseScope(u))
		}
		b.WriteString("\t\t},\n")
	}
	if len(plan.Extractors) > 0 {
		fmt.Fprintf(&b, "\t\tExtractors: []%s.GraphExtractor{\n", e.servoAlias)
		for _, ex := range plan.Extractors {
			fmt.Fprintf(&b, "\t\t\t{Type: %q, Produces: %q},\n", ex.Node.Key.String(), ex.Produces.String())
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}")
	return b.String()
}

func (e *emitter) writeGraphNode(b *strings.Builder, n *resolve.Node, level int, scope string) {
	deps := make([]string, len(n.Deps))
	for i, d := range n.Deps {
		deps[i] = d.Key.String()
	}
	fmt.Fprintf(b, "\t\t{Type: %q, Level: %d, Deps: %s, Capabilities: %s, Binding: %q, Pos: %q",
		n.Key.String(), level, stringSliceLiteral(deps), stringSliceLiteral(n.Capabilities), n.Binding, e.posString(n.Provider.Pos))
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
