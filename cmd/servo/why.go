package main

import (
	"fmt"
	"sort"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// whyPathLimit caps `why --all`. Paths through a densely shared graph
// multiply — every diamond doubles them — and past a hundred the answer is
// no longer something to read; explain on an intermediate node is.
const whyPathLimit = 100

// whyPath is one answer to `why`: the nodes from where the path starts
// down to the node asked about, and the HTTP declaration it starts from
// when that is not a servo.Root.
type whyPath struct {
	entry *httpEntry
	nodes []*resolve.Node
}

// runWhy answers "why is this even in my binary": the shortest path from
// any root to the named node.
func runWhy(cfg load.Config, query string, jsonOut bool) error {
	return runWhyPaths(cfg, query, jsonOut, false)
}

// runWhyPaths is runWhy, or with all, `why --all`: every path rather
// than the shortest one.
func runWhyPaths(cfg load.Config, query string, jsonOut, all bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	hops := interfaceHops(p.spec, resolved)
	target, _, err := lookupNode(p, resolved, hops, query)
	if err != nil {
		return err
	}

	var paths []whyPath
	more := false
	if all {
		paths, more = allPathsTo(resolved, target, whyPathLimit)
	} else if path, ok := shortestPathFromRoot(resolved, target); ok {
		paths = []whyPath{{nodes: path}}
	} else if path, entry, ok := shortestPathFromHTTP(resolved, target); ok {
		// A node only the HTTP surface reaches — a handler's dependency,
		// a middleware, an extractor, the server config — descends from no
		// servo.Root. The declaration that pulled it in is the answer, and
		// the resolver recorded it.
		paths = []whyPath{{entry: entry, nodes: path}}
	}
	if len(paths) == 0 {
		return fmt.Errorf("servo why: %s is not reachable from any root", target.Key)
	}

	if jsonOut {
		// Without --all the schema is what it always was, one path as an
		// array of strings; --all is an array of those.
		names := make([][]string, len(paths))
		for i, path := range paths {
			if path.entry != nil {
				names[i] = append(names[i], path.entry.label)
			}
			for _, n := range path.nodes {
				names[i] = append(names[i], n.Key.String())
			}
		}
		if !all {
			return printJSON(names[0])
		}
		return printJSON(names)
	}

	for i, path := range paths {
		if i > 0 {
			fmt.Println()
		}
		printWhyPath(p.spec, resolved, hops, path)
	}
	if more {
		fmt.Printf("\n… and more: only the first %d paths are shown — `servo explain` on a node along them narrows the question\n", whyPathLimit)
	}
	return nil
}

// printWhyPath prints one path, each step naming what was asked for when
// it differs from what was handed over: api.Server takes store.Store, not
// *postgres.DB, and a path that skipped the interface would answer "why"
// with a parameter list nobody wrote.
func printWhyPath(spec *load.Spec, resolved *resolve.Resolved, hops map[graph.Key]*hop, path whyPath) {
	for i, n := range path.nodes {
		var requested graph.Key
		var known bool
		switch {
		case i > 0:
			requested, known = requestedAs(path.nodes[i-1], n)
		case path.entry != nil:
			requested, known = path.entry.requested, true
		default:
			requested, known = rootRequest(spec, resolved, n)
		}
		note := ""
		if known {
			note = hopNote(hops, requested, n)
		}
		switch {
		case i == 0 && path.entry != nil:
			fmt.Printf("root  %s\n  -> %s%s\n", path.entry.label, n.Key.String(), note)
		case i == 0:
			fmt.Printf("root  %s%s\n", n.Key.String(), note)
		default:
			fmt.Printf("  -> %s%s\n", n.Key.String(), note)
		}
	}
}

// requestedAs is the key consumer asked for when the graph handed it dep:
// a constructor parameter, a scope accessor `why` walks straight through
// to the instance it hands out, or the implementation behind a trace
// wrapper.
func requestedAs(consumer, dep *resolve.Node) (graph.Key, bool) {
	if consumer.Kind == resolve.NodeTraced && consumer.Traced != nil && consumer.Traced.Inner == dep {
		return graph.Key{Type: consumer.Key.Type}, true
	}
	if consumer.Provider == nil || len(consumer.Provider.Params) != len(consumer.Deps) {
		return graph.Key{}, false
	}
	for i, d := range consumer.Deps {
		if d == dep || (d.Kind == resolve.NodeScopeAccessor && d.ScopeRoot != nil && d.ScopeRoot.Node == dep) {
			return consumer.Provider.Params[i], true
		}
	}
	return graph.Key{}, false
}

// rootRequest is the type the servo.Root behind root named — an
// interface whenever the spec declares one as a root.
func rootRequest(spec *load.Spec, resolved *resolve.Resolved, root *resolve.Node) (graph.Key, bool) {
	if len(spec.Roots) != len(resolved.Roots) {
		return graph.Key{}, false
	}
	for i, n := range resolved.Roots {
		if n == root {
			return spec.Roots[i].Key, true
		}
	}
	return graph.Key{}, false
}

// hopNote annotates a step whose request named an interface, or a scope
// accessor, rather than the node it got.
func hopNote(hops map[graph.Key]*hop, requested graph.Key, got *resolve.Node) string {
	h := hops[requested]
	if requested == got.Key || h == nil || h.node != got {
		return ""
	}
	if h.binding == bindingAccessor {
		return "  (acquired via " + requested.String() + ")"
	}
	return "  (as " + requested.String() + ", " + h.binding + ")"
}

// httpEntry is one node the emitted HTTP servers pull into the graph
// directly, with the declaration that did it, phrased the way the
// resolver's needed-by chains phrase it, and the type that declaration
// asked for — an interface, when a handler takes one.
type httpEntry struct {
	node      *resolve.Node
	label     string
	requested graph.Key
}

// httpEntryPoints lists the servers' root-like requirements in plan order:
// the config, every handler's dependencies, the middleware, the
// extractors. `why` searches from them when no servo.Root reaches a node,
// and `explain` lists them among a node's dependents.
func httpEntryPoints(resolved *resolve.Resolved) []httpEntry {
	plan := resolved.HTTP
	if plan == nil {
		return nil
	}
	var out []httpEntry
	if plan.Config != nil {
		out = append(out, httpEntry{plan.Config, "servo.HTTP(), the server config", plan.Config.Key})
	}
	for _, hr := range plan.Routes {
		for i, arg := range hr.Args {
			if arg.Node == nil {
				continue
			}
			requested := arg.Node.Key
			if len(hr.Route.Deps) == len(hr.Args) {
				requested = hr.Route.Deps[i]
			}
			out = append(out, httpEntry{arg.Node, fmt.Sprintf("handler %s, %s %s", hr.Route.Name, hr.Route.Method, hr.Route.Pattern), requested})
		}
	}
	for _, u := range plan.Uses {
		out = append(out, httpEntry{u.Node, fmt.Sprintf("servo.Use[%s], %s", u.Node.Key.String(), resolve.HTTPUseScope(u)), u.Decl.Type})
	}
	for _, ex := range plan.Extractors {
		out = append(out, httpEntry{ex.Node, fmt.Sprintf("servo.Extract[%s]", ex.Node.Key.String()), ex.Node.Key})
	}
	return out
}

// shortestPathFromHTTP is shortestPathFromRoot with the HTTP entry points
// as the sources, tried one at a time so the path comes back with the
// declaration it starts from.
func shortestPathFromHTTP(resolved *resolve.Resolved, target *resolve.Node) ([]*resolve.Node, *httpEntry, bool) {
	for _, entry := range httpEntryPoints(resolved) {
		if path, ok := shortestPath([]*resolve.Node{entry.node}, target); ok {
			return path, &entry, true
		}
	}
	return nil, nil, false
}

// shortestPathFromRoot runs multi-source BFS from every root simultaneously
// — the graph is acyclic (cycles are a build-time diagnostic, never
// reached here), so the first time BFS reaches target is a shortest path.
func shortestPathFromRoot(resolved *resolve.Resolved, target *resolve.Node) ([]*resolve.Node, bool) {
	return shortestPath(resolved.Roots, target)
}

// shortestPath is the BFS itself, from any set of start nodes.
func shortestPath(starts []*resolve.Node, target *resolve.Node) ([]*resolve.Node, bool) {
	type queued struct {
		node *resolve.Node
		path []*resolve.Node
	}

	visited := map[graph.Key]bool{}
	var queue []queued
	for _, r := range starts {
		if visited[r.Key] {
			continue
		}
		visited[r.Key] = true
		queue = append(queue, queued{r, []*resolve.Node{r}})
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.node.Key == target.Key {
			return item.path, true
		}
		for _, dep := range scopeAwareDeps(item.node) {
			if visited[dep.Key] {
				continue
			}
			visited[dep.Key] = true
			path := append(append([]*resolve.Node{}, item.path...), dep)
			queue = append(queue, queued{dep, path})
		}
	}
	return nil, false
}

// allPathsTo is every path to target from a root, then from each HTTP
// declaration that pulls it in — every reason it is in the binary —
// shortest first, at most limit of them; more reports that the limit cut
// some off. The graph is acyclic, so a depth-first walk that only enters
// nodes from which target is reachable terminates and never wanders down
// a branch that cannot end at it.
func allPathsTo(resolved *resolve.Resolved, target *resolve.Node, limit int) (paths []whyPath, more bool) {
	reaches := map[graph.Key]bool{}
	var canReach func(n *resolve.Node) bool
	canReach = func(n *resolve.Node) bool {
		if n.Key == target.Key {
			return true
		}
		if r, seen := reaches[n.Key]; seen {
			return r
		}
		reaches[n.Key] = false
		for _, dep := range scopeAwareDeps(n) {
			if canReach(dep) {
				reaches[n.Key] = true
				break
			}
		}
		return reaches[n.Key]
	}

	var walk func(entry *httpEntry, path []*resolve.Node) bool
	walk = func(entry *httpEntry, path []*resolve.Node) bool {
		n := path[len(path)-1]
		if n.Key == target.Key {
			if len(paths) == limit {
				more = true
				return false
			}
			paths = append(paths, whyPath{entry: entry, nodes: append([]*resolve.Node(nil), path...)})
			return true
		}
		for _, dep := range scopeAwareDeps(n) {
			if canReach(dep) && !walk(entry, append(path, dep)) {
				return false
			}
		}
		return true
	}

	seen := map[graph.Key]bool{}
	for _, r := range resolved.Roots {
		if seen[r.Key] || !canReach(r) {
			continue
		}
		seen[r.Key] = true
		if !walk(nil, []*resolve.Node{r}) {
			break
		}
	}
	if !more {
		for _, e := range httpEntryPoints(resolved) {
			entry := e
			if !canReach(entry.node) {
				continue
			}
			if !walk(&entry, []*resolve.Node{entry.node}) {
				break
			}
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i].nodes) < len(paths[j].nodes) })
	return paths, more
}

// scopeAwareDeps follows a dependency on a scope's accessor through to the
// instances it hands out. An accessor is generated code rather than a
// resolved node, so a plain walk over Deps would stop at the interface and
// report every scoped node as unreachable from any root — which is exactly
// backwards: they are in the binary *because* something acquires them.
func scopeAwareDeps(n *resolve.Node) []*resolve.Node {
	var out []*resolve.Node
	for _, dep := range n.Deps {
		switch dep.Kind {
		case resolve.NodeScopeAccessor:
			if dep.ScopeRoot != nil && dep.ScopeRoot.Node != nil {
				out = append(out, dep.ScopeRoot.Node)
			}
		case resolve.NodeProvider, resolve.NodeSupplied, resolve.NodeConfig, resolve.NodeTraced:
			// A supplied value or a config is a real edge — the consumer
			// depends on it exactly as it would on a constructed one — so
			// `why` has to traverse it or report a node it can plainly see
			// as unreachable. NodeScopeKey is still skipped: it has no
			// provider and no declaration site to lead anywhere.
			out = append(out, dep)
		}
	}
	return out
}
