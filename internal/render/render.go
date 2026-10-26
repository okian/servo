// Package render turns a resolved graph into the human/machine formats
// `servo graph` exports: text, JSON, DOT, and Mermaid.
package render

import (
	"go/token"
	"sort"

	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
	"github.com/okian/servo/v3/servo"
)

// ToGraph converts a resolved graph into the servo.Graph `servo graph`
// exports, with every position filled in. It is also, positions aside,
// exactly what the generated App.Graph() returns: the emitter writes that
// method's literal from Committed, the same conversion, so the build-time
// and runtime views are one value printed twice rather than two
// converters kept in step by hand — which they once were, and they
// disagreed about the order of a scope's borrows.
//
// positions renders each position relative to the module root inside the
// module and by import path outside it (see load.Positions), so the output
// is the same on every checkout. nil leaves them absolute, which is what a
// Resolved built by hand in a test gets.
func ToGraph(resolved *resolve.Resolved, positions *load.Positions) servo.Graph {
	return toGraph(resolved, func(pos token.Position) string { return positions.String(pos) })
}

// Committed is the graph as the generated App.Graph() carries it: ToGraph
// with every Pos left empty. A position moves whenever a line is added
// above a constructor, and a standard-library provider's moves with the
// toolchain's patch release, so a committed file that carried positions
// went stale — and failed `servo check` — on edits that changed nothing
// about the graph, and teammates on different Go releases kept rewriting
// each other's copy. GraphNode.Provider names what builds each node
// instead, and `servo graph`, `explain` and `why` still print the live
// positions.
func Committed(resolved *resolve.Resolved) servo.Graph {
	return toGraph(resolved, func(token.Position) string { return "" })
}

func toGraph(resolved *resolve.Resolved, pos func(token.Position) string) servo.Graph {
	nodes := make([]servo.GraphNode, 0, len(resolved.Order)+len(resolved.Supplied)+len(resolved.Configs))
	// Supplied values and configs first, at level 0: the app depends on
	// them before it builds anything.
	for _, n := range resolved.Supplied {
		nodes = append(nodes, graphNode(n, 0, "", pos))
	}
	for _, n := range resolved.Configs {
		nodes = append(nodes, graphNode(n, 0, "", pos))
	}
	for _, n := range resolved.Order {
		nodes = append(nodes, graphNode(n, n.Level, "", pos))
	}
	// Scoped members come after the singletons, each carrying its scope's
	// key and its level within that scope rather than within the app.
	var scopes []servo.GraphScope
	for _, s := range resolved.Scopes {
		for _, n := range s.Order {
			nodes = append(nodes, graphNode(n, n.ScopeLevel, s.KeyKey.String(), pos))
		}
		scopes = append(scopes, graphScope(s))
	}
	return servo.Graph{Nodes: nodes, Scopes: scopes, HTTP: graphHTTP(resolved.HTTP, pos)}
}

// graphHTTP is the emitted HTTP plan as data, routes in the plan's own
// order.
func graphHTTP(plan *resolve.HTTPPlan, pos func(token.Position) string) *servo.GraphHTTP {
	if plan == nil {
		return nil
	}
	out := &servo.GraphHTTP{Groups: append([]string(nil), plan.Groups...)}
	for _, hr := range plan.Routes {
		out.Routes = append(out.Routes, servo.GraphRoute{
			Method:  hr.Route.Method,
			Pattern: hr.Route.Pattern,
			Group:   hr.Route.Group,
			Handler: hr.Route.Name,
			Args:    resolve.HTTPRouteArgs(hr),
			Pos:     pos(hr.Route.Pos),
		})
	}
	for _, u := range plan.Uses {
		out.Uses = append(out.Uses, servo.GraphUse{Type: u.Node.Key.String(), Scope: resolve.HTTPUseScope(u)})
	}
	for _, ex := range plan.Extractors {
		out.Extractors = append(out.Extractors, servo.GraphExtractor{Type: ex.Node.Key.String(), Produces: ex.Produces.String()})
	}
	return out
}

func graphNode(n *resolve.Node, level int, scope string, pos func(token.Position) string) servo.GraphNode {
	// nil rather than an empty slice: the doc promises `servo graph` and
	// App.Graph() one document, and a consumer iterating node.deps finds
	// out the hard way when `[]` from one producer meets `null` from the
	// other. The emitter prints whatever this holds, nil included.
	var deps []string
	for _, d := range n.Deps {
		deps = append(deps, d.Key.String())
	}
	binding := n.Binding
	at := ""
	provider := ""
	wraps := ""
	switch n.Kind {
	case resolve.NodeSupplied:
		binding = "supplied"
		at = pos(n.SuppliedPos)
	case resolve.NodeConfig:
		at = pos(n.Config.Pos)
		provider = n.Config.PkgName + "." + ConfigLoaderFuncName
	case resolve.NodeTraced:
		at = pos(n.Traced.Decl.Pos)
		wraps = n.Traced.Inner.Key.String()
	default:
		at = pos(n.Provider.Pos)
		provider = n.Provider.Name
	}
	return servo.GraphNode{
		Type:         n.Key.String(),
		Level:        level,
		Deps:         deps,
		Capabilities: append([]string(nil), n.Capabilities...),
		Binding:      binding,
		Pos:          at,
		Provider:     provider,
		Scope:        scope,
		Wraps:        wraps,
	}
}

// ConfigLoaderFuncName is the loader every //servo:config type's companion
// file declares, which GraphNode.Provider names for a config node. It is
// the emitter's to choose; it lives here because emit builds on this
// package rather than the reverse, and emit's own constant is defined as
// this one.
const ConfigLoaderFuncName = "ServoConfig"

func graphScope(s *resolve.Scope) servo.GraphScope {
	accessors := make([]string, len(s.Roots))
	for i, root := range s.Roots {
		accessors[i] = root.Iface.String()
	}
	members := make([]string, len(s.Order))
	for i, n := range s.Order {
		members[i] = n.Key.String()
	}
	borrows := borrowedOf(s)
	return servo.GraphScope{
		Key: s.KeyKey.String(), Linger: s.Linger.String(), Max: s.Max,
		Accessors: accessors, Members: members, Borrows: borrows,
	}
}

// borrowedOf lists the singletons a scope's members and extractors depend
// on. They are constructed once by the App and shared by every instance,
// so they are reported separately from the members rather than counted as
// part of what an instance holds. Ordered by level, stably, which is the
// order the generated file's header lists them in and the scope's
// teardown is computed from.
func borrowedOf(s *resolve.Scope) []string {
	seen := map[string]bool{}
	var nodes []*resolve.Node
	add := func(n *resolve.Node) {
		if n == nil || (n.Kind != resolve.NodeProvider && n.Kind != resolve.NodeTraced) || n.Scope != nil || seen[n.Key.String()] {
			return
		}
		seen[n.Key.String()] = true
		nodes = append(nodes, n)
	}
	for _, n := range s.Order {
		for _, d := range n.Deps {
			add(d)
		}
	}
	for _, root := range s.Roots {
		for _, d := range root.ExtractorDeps {
			add(d)
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Level < nodes[j].Level })
	var out []string
	for _, n := range nodes {
		out = append(out, n.Key.String())
	}
	return out
}

// byLevel groups g's nodes by level, ascending, each level's nodes kept in
// their original relative order.
func byLevel(g servo.Graph) (levels []int, grouped map[int][]servo.GraphNode) {
	grouped = map[int][]servo.GraphNode{}
	for _, n := range g.Nodes {
		if _, ok := grouped[n.Level]; !ok {
			levels = append(levels, n.Level)
		}
		grouped[n.Level] = append(grouped[n.Level], n)
	}
	sort.Ints(levels)
	return levels, grouped
}

var levelPalette = []string{"#dbeafe", "#bfdbfe", "#93c5fd", "#60a5fa", "#3b82f6", "#2563eb", "#1d4ed8"}

func colorFor(level int) string {
	return levelPalette[level%len(levelPalette)]
}

// accessorAlias maps each scope's accessor interfaces onto that scope's
// key. An accessor is not a node — it is generated code, not a resolved
// provider — so a consumer's edge to one would otherwise dangle. Pointing
// it at the key instead draws the edge that is actually true: this
// singleton reaches instances chosen by that key.
func accessorAlias(g servo.Graph) map[string]string {
	alias := map[string]string{}
	for _, s := range g.Scopes {
		for _, a := range s.Accessors {
			alias[a] = s.Key
		}
	}
	return alias
}

func resolveDep(alias map[string]string, dep string) string {
	if target, ok := alias[dep]; ok {
		return target
	}
	return dep
}
