package main

import "fmt"

type explainJSON struct {
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	Pos          string   `json:"pos"`
	Binding      string   `json:"binding"`
	Level        int      `json:"level"`
	DependsOn    []string `json:"depends_on"`
	DependedOn   []string `json:"depended_on"`
	Capabilities []string `json:"capabilities"`
}

// runExplain answers, for one node: which provider was selected and why,
// its position, direct dependencies, dependents, level, and capabilities.
func runExplain(dir, query string, jsonOut bool) error {
	p, err := buildPipeline(dir)
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
	for _, n := range resolved.Order {
		for _, d := range n.Deps {
			if d.Key == node.Key {
				dependents = append(dependents, n.Key.String())
			}
		}
	}

	if jsonOut {
		return printJSON(explainJSON{
			Type: node.Key.String(), Provider: node.Provider.Name, Pos: node.Provider.Pos.String(),
			Binding: node.Binding, Level: node.Level,
			DependsOn: deps, DependedOn: dependents, Capabilities: node.Capabilities,
		})
	}

	fmt.Printf("%s\n", node.Key.String())
	fmt.Printf("  provider:     %s (%s)\n", node.Provider.Name, node.Provider.Pos)
	fmt.Printf("  binding:      %s\n", node.Binding)
	fmt.Printf("  level:        %d\n", node.Level)
	fmt.Printf("  depends on:   %s\n", joinOrNone(deps))
	fmt.Printf("  depended on:  %s\n", joinOrNone(dependents))
	fmt.Printf("  capabilities: %s\n", joinOrNone(node.Capabilities))
	return nil
}
