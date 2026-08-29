package main

import (
	"fmt"

	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/render"
)

// runGraph exports the resolved graph. JSON is the stable machine format;
// text/dot/mermaid are for humans and docs.
func runGraph(cfg load.Config, format string) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	g := render.ToGraph(resolved)

	switch format {
	case "", "text":
		fmt.Print(render.Text(g))
	case "json":
		out, err := render.JSON(g)
		if err != nil {
			return err
		}
		fmt.Print(out)
	case "dot":
		fmt.Print(render.DOT(g))
	case "mermaid":
		fmt.Print(render.Mermaid(g))
	default:
		return fmt.Errorf("servo graph: unknown --format %q (want text|json|dot|mermaid)", format)
	}
	return nil
}
