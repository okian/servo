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
	if !ok {
		return fmt.Errorf("servo why: %s is not reachable from any root", target.Key)
	}

	if jsonOut {
		names := make([]string, len(path))
		for i, n := range path {
			names[i] = n.Key.String()
		}
		return printJSON(names)
	}

	for i, n := range path {
		if i == 0 {
			fmt.Printf("root  %s\n", n.Key.String())
			continue
		}
		fmt.Printf("  -> %s\n", n.Key.String())
	}
	return nil
}

// shortestPathFromRoot runs multi-source BFS from every root simultaneously
// — the graph is acyclic (cycles are a build-time diagnostic, never
// reached here), so the first time BFS reaches target is a shortest path.
func shortestPathFromRoot(resolved *resolve.Resolved, target *resolve.Node) ([]*resolve.Node, bool) {
	type queued struct {
		node *resolve.Node
		path []*resolve.Node
	}

	visited := map[graph.Key]bool{}
	var queue []queued
	for _, r := range resolved.Roots {
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
		case resolve.NodeProvider, resolve.NodeSupplied, resolve.NodeConfig:
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
