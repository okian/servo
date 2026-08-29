package main

import (
	"fmt"

	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

type explainJSON struct {
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	Pos          string   `json:"pos"`
	Binding      string   `json:"binding"`
	Level        int      `json:"level"`
	DependsOn    []string `json:"depends_on"`
	DependedOn   []string `json:"depended_on"`
	Capabilities []string `json:"capabilities"`
	// Scope is the key type this node is one instance per, empty for an
	// ordinary singleton; Lifetime spells the same thing out in words.
	Scope    string `json:"scope,omitempty"`
	Lifetime string `json:"lifetime"`
}

// runExplain answers, for one node: which provider was selected and why,
// its position, direct dependencies, dependents, level, and capabilities.
func runExplain(cfg load.Config, query string, jsonOut bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	node, err := findNode(resolved, query)
	if err != nil {
		return err
	}

	deps := make([]string, len(node.Deps))
	for i, d := range node.Deps {
		deps[i] = d.Key.String()
	}
	var dependents []string
	for _, n := range allNodes(resolved) {
		for _, d := range n.Deps {
			if d.Key == node.Key {
				dependents = append(dependents, n.Key.String())
			}
		}
	}
	// A scoped node's consumers are usually not other nodes at all: they
	// hold the accessor interface and acquire per call, so name that
	// instead of reporting "none" for something plainly in use.
	scopeKey, lifetime := "", "singleton — one per process, built by New"
	if node.Scoped() {
		scopeKey = node.Scope.KeyKey.String()
		lifetime = fmt.Sprintf("scoped — one per %s, linger %s, max %d", scopeKey, node.Scope.Linger, node.Scope.Max)
		for _, root := range node.Scope.Roots {
			if root.Node == node {
				dependents = append(dependents, "(acquired via "+root.Iface.String()+")")
			}
		}
	}

	if jsonOut {
		return printJSON(explainJSON{
			Type: node.Key.String(), Provider: node.Provider.Name, Pos: node.Provider.Pos.String(),
			Binding: node.Binding, Level: levelOf(node),
			DependsOn: deps, DependedOn: dependents, Capabilities: node.Capabilities,
			Scope: scopeKey, Lifetime: lifetime,
		})
	}

	fmt.Printf("%s\n", node.Key.String())
	fmt.Printf("  provider:     %s (%s)\n", node.Provider.Name, node.Provider.Pos)
	fmt.Printf("  binding:      %s\n", node.Binding)
	fmt.Printf("  lifetime:     %s\n", lifetime)
	fmt.Printf("  level:        %d\n", levelOf(node))
	fmt.Printf("  depends on:   %s\n", joinOrNone(deps))
	fmt.Printf("  depended on:  %s\n", joinOrNone(dependents))
	fmt.Printf("  capabilities: %s\n", joinOrNone(node.Capabilities))
	return nil
}

// levelOf reports a scoped node's level within its own scope, and an
// ordinary node's within the app. Reporting a scoped node's app-level
// depth would say more about how deep the singletons it borrows sit than
// about when it is constructed.
func levelOf(n *resolve.Node) int {
	if n.Scoped() {
		return n.ScopeLevel
	}
	return n.Level
}
