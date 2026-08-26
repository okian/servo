package main

import (
	"fmt"

	"github.com/okian/servo/v2/internal/graph"
	"github.com/okian/servo/v2/internal/resolve"
)

// runWhy answers "why is this even in my binary": the shortest path from
// any root to the named node.
func runWhy(dir, query string, jsonOut bool) error {
	p, err := buildPipeline(dir)
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
		for _, dep := range item.node.Deps {
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
