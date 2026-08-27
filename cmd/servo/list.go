package main

import (
	"encoding/json"
	"fmt"

	"github.com/okian/servo/v3/internal/graph"
)

// runList dumps the candidate index, or with rejected=true, every function
// that was NOT indexed and the rule that excluded it — the highest-value
// diagnostic for "I wrote a constructor and servo doesn't see it." Defaults
// to the main module only: the underlying scan is deliberately broad (it
// covers the whole transitive dependency graph, including stdlib), but a
// human asking "why wasn't my function picked up" is never asking about
// unicode.ToLower — showAll opts back into the full, unfiltered index.
func runList(dir string, rejected, showAll, jsonOut bool) error {
	p, err := buildPipeline(dir)
	if err != nil {
		return err
	}

	if rejected {
		type rejectedJSON struct {
			Name   string `json:"name"`
			Pos    string `json:"pos"`
			Reason string `json:"reason"`
		}
		filtered := filterRejected(p.rejected, p.scope, showAll)
		if jsonOut {
			out := make([]rejectedJSON, len(filtered))
			for i, r := range filtered {
				out[i] = rejectedJSON{r.Name, r.Pos.String(), r.Reason}
			}
			return printJSON(out)
		}
		for _, r := range filtered {
			fmt.Printf("%-30s %-30s %s\n", r.Name, r.Pos.String(), r.Reason)
		}
		return nil
	}

	type candidateJSON struct {
		Name string `json:"name"`
		Pos  string `json:"pos"`
	}
	filtered := filterCandidates(p.candidates, p.scope, showAll)
	if jsonOut {
		out := make([]candidateJSON, len(filtered))
		for i, c := range filtered {
			out[i] = candidateJSON{c.Name, c.Pos.String()}
		}
		return printJSON(out)
	}
	for _, c := range filtered {
		fmt.Printf("%-30s %s\n", c.Name, c.Pos.String())
	}
	return nil
}

func filterRejected(rejected []graph.Rejected, scope map[string]bool, showAll bool) []graph.Rejected {
	if showAll {
		return rejected
	}
	var out []graph.Rejected
	for _, r := range rejected {
		if scope[r.Pkg] {
			out = append(out, r)
		}
	}
	return out
}

func filterCandidates(candidates []*graph.Provider, scope map[string]bool, showAll bool) []*graph.Provider {
	if showAll {
		return candidates
	}
	var out []*graph.Provider
	for _, c := range candidates {
		if scope[c.Pkg] {
			out = append(out, c)
		}
	}
	return out
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
