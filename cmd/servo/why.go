package main

import (
	"fmt"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// runWhy answers "why is this even in my binary": the shortest path from
// any root to the named node.
func runWhy(cfg load.Config, query string, jsonOut bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	target, err := findNode(resolved, query)
	if err != nil {
		return err
	}

	path, ok := shortestPathFromRoot(resolved, target)
	// A node only the HTTP surface reaches — a handler's dependency, a
	// middleware, an extractor, the server config — descends from no
	// servo.Root. The declaration that pulled it in is the answer, and the
	// resolver recorded it.
	entry := ""
	if !ok {
		path, entry, ok = shortestPathFromHTTP(resolved, target)
	}
	if !ok {
		return fmt.Errorf("servo why: %s is not reachable from any root", target.Key)
	}

	if jsonOut {
		var names []string
		if entry != "" {
			names = append(names, entry)
		}
		for _, n := range path {
			names = append(names, n.Key.String())
		}
		return printJSON(names)
	}

	for i, n := range path {
		switch {
		case i == 0 && entry != "":
			fmt.Printf("root  %s\n  -> %s\n", entry, n.Key.String())
		case i == 0:
			fmt.Printf("root  %s\n", n.Key.String())
		default:
			fmt.Printf("  -> %s\n", n.Key.String())
		}
	}
	return nil
}

// httpEntry is one node the emitted HTTP servers pull into the graph
// directly, with the declaration that did it, phrased the way the
// resolver's needed-by chains phrase it.
type httpEntry struct {
	node  *resolve.Node
	label string
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
		out = append(out, httpEntry{plan.Config, "servo.HTTP(), the server config"})
	}
	for _, hr := range plan.Routes {
		for _, arg := range hr.Args {
			if arg.Node != nil {
				out = append(out, httpEntry{arg.Node, fmt.Sprintf("handler %s, %s %s", hr.Route.Name, hr.Route.Method, hr.Route.Pattern)})
			}
		}
	}
	for _, u := range plan.Uses {
		out = append(out, httpEntry{u.Node, fmt.Sprintf("servo.Use[%s], %s", u.Node.Key.String(), resolve.HTTPUseScope(u))})
	}
	for _, ex := range plan.Extractors {
		out = append(out, httpEntry{ex.Node, fmt.Sprintf("servo.Extract[%s]", ex.Node.Key.String())})
	}
	return out
}

// shortestPathFromHTTP is shortestPathFromRoot with the HTTP entry points
// as the sources, tried one at a time so the path comes back with the
// declaration it starts from.
func shortestPathFromHTTP(resolved *resolve.Resolved, target *resolve.Node) ([]*resolve.Node, string, bool) {
	for _, entry := range httpEntryPoints(resolved) {
		if path, ok := shortestPath([]*resolve.Node{entry.node}, target); ok {
			return path, entry.label, true
		}
	}
	return nil, "", false
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
