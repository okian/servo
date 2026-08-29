package main

import "strings"

func nodeTypes(app *App) []string {
	var out []string
	for _, n := range app.Graph().Nodes {
		out = append(out, n.Type)
	}
	return out
}

func containsSuffix(types []string, suffix string) bool {
	for _, t := range types {
		if strings.HasSuffix(t, suffix) {
			return true
		}
	}
	return false
}
