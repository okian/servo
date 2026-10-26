package emit

import (
	"fmt"
	"strings"

	"github.com/okian/servo/v3/internal/render"
	"github.com/okian/servo/v3/servo"
)

// graphFunc emits App.Graph() as a compile-time constant: nodes, edges,
// levels, and capabilities as plain data, never indexable back to an
// instance.
//
// The data is render.Committed's, printed: the conversion `servo graph`
// uses, minus positions, so the build-time and runtime views are one value
// rather than two converters kept in step by hand. They used to be two,
// and disagreed about the order of a scope's borrows and of routes across
// groups. Every field is printed exactly as the value holds it, a nil
// slice as nil and an empty one as empty, so the two serialize alike too.
func (e *emitter) graphFunc() string {
	g := render.Committed(e.resolved)
	var b strings.Builder
	fmt.Fprintf(&b, "func (a *%s) Graph() %s.Graph {\n", e.appType(), e.servoAlias)
	fmt.Fprintf(&b, "\treturn %s.Graph{Nodes: []%s.GraphNode{\n", e.servoAlias, e.servoAlias)
	for _, n := range g.Nodes {
		e.writeGraphNode(&b, n)
	}
	b.WriteString("\t}")
	b.WriteString(e.graphScopes(g.Scopes))
	b.WriteString(e.graphHTTP(g.HTTP))
	b.WriteString("}\n}\n\n")
	return b.String()
}

// writeGraphNode prints one node. Pos is never set here — Committed leaves
// it empty — and the fields after Binding are omitted at their zero value,
// so a node with no provider, scope or wrapped node reads as it always
// did.
func (e *emitter) writeGraphNode(b *strings.Builder, n servo.GraphNode) {
	fmt.Fprintf(b, "\t\t{Type: %q, Level: %d, Deps: %s, Capabilities: %s, Binding: %q",
		n.Type, n.Level, goStrings(n.Deps), goStrings(n.Capabilities), n.Binding)
	if n.Pos != "" {
		fmt.Fprintf(b, ", Pos: %q", n.Pos)
	}
	if n.Provider != "" {
		fmt.Fprintf(b, ", Provider: %q", n.Provider)
	}
	if n.Scope != "" {
		fmt.Fprintf(b, ", Scope: %q", n.Scope)
	}
	if n.Wraps != "" {
		fmt.Fprintf(b, ", Wraps: %q", n.Wraps)
	}
	b.WriteString("},\n")
}

// graphScopes emits the Scopes field only when there is one, so an app
// with nothing scoped produces the same literal it always did.
func (e *emitter) graphScopes(scopes []servo.GraphScope) string {
	if scopes == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, ", Scopes: []%s.GraphScope{\n", e.servoAlias)
	for _, s := range scopes {
		fmt.Fprintf(&b, "\t\t{Key: %q, Linger: %q, Max: %d, Accessors: %s, Members: %s, Borrows: %s},\n",
			s.Key, s.Linger, s.Max, goStrings(s.Accessors), goStrings(s.Members), goStrings(s.Borrows))
	}
	b.WriteString("\t}")
	return b.String()
}

// graphHTTP emits the HTTP field only when the spec declares servo.HTTP(),
// so a plan-less app produces the same literal it always did.
func (e *emitter) graphHTTP(h *servo.GraphHTTP) string {
	if h == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, ", HTTP: &%s.GraphHTTP{\n", e.servoAlias)
	if h.Groups != nil {
		fmt.Fprintf(&b, "\t\tGroups: %s,\n", goStrings(h.Groups))
	}
	if h.Routes != nil {
		fmt.Fprintf(&b, "\t\tRoutes: []%s.GraphRoute{\n", e.servoAlias)
		for _, rt := range h.Routes {
			fmt.Fprintf(&b, "\t\t\t{Method: %q, Pattern: %q", rt.Method, rt.Pattern)
			if rt.Group != "" {
				fmt.Fprintf(&b, ", Group: %q", rt.Group)
			}
			fmt.Fprintf(&b, ", Handler: %q", rt.Handler)
			if rt.Args != nil {
				fmt.Fprintf(&b, ", Args: %s", goStrings(rt.Args))
			}
			if rt.Pos != "" {
				fmt.Fprintf(&b, ", Pos: %q", rt.Pos)
			}
			b.WriteString("},\n")
		}
		b.WriteString("\t\t},\n")
	}
	if h.Uses != nil {
		fmt.Fprintf(&b, "\t\tUses: []%s.GraphUse{\n", e.servoAlias)
		for _, u := range h.Uses {
			fmt.Fprintf(&b, "\t\t\t{Type: %q, Scope: %q},\n", u.Type, u.Scope)
		}
		b.WriteString("\t\t},\n")
	}
	if h.Extractors != nil {
		fmt.Fprintf(&b, "\t\tExtractors: []%s.GraphExtractor{\n", e.servoAlias)
		for _, ex := range h.Extractors {
			fmt.Fprintf(&b, "\t\t\t{Type: %q, Produces: %q},\n", ex.Type, ex.Produces)
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}")
	return b.String()
}

// goStrings prints ss as the Go expression that rebuilds it exactly: nil
// for a nil slice, []string{} for an empty one. JSON tells the two apart
// (null and []), so the literal must too, or App.Graph() would serialize
// differently from the value it was printed from.
func goStrings(ss []string) string {
	if ss == nil {
		return "nil"
	}
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// reportFunc exposes the per-node init timings the constructor recorded,
// with no separate runtime traversal.
func (e *emitter) reportFunc() string {
	return fmt.Sprintf("func (a *%s) Report() %s.StartupReport {\n\treturn a.startupReport\n}\n\n", e.appType(), e.servoAlias)
}
