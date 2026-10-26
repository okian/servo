package main

import (
	"encoding/json"
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// runList dumps the candidate index, or with rejected=true, every function
// that was NOT indexed and the rule that excluded it — the highest-value
// diagnostic for "I wrote a constructor and servo doesn't see it." Defaults
// to the main module only: the underlying scan is deliberately broad (it
// covers the whole transitive dependency graph, including stdlib), but a
// human asking "why wasn't my function picked up" is never asking about
// unicode.ToLower — showAll opts back into the full, unfiltered index.
func runList(cfg load.Config, rejected, showAll, jsonOut bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}

	if rejected {
		return listRejected(p, showAll, jsonOut)
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

	// Routes are the other thing servo sees, and "why doesn't servo serve
	// my endpoint" is the same question as "why isn't my constructor
	// indexed". Module-wide like the scan itself; the JSON schema stays the
	// bare candidate array it has always been (documented in cli.md).
	if len(p.routes) > 0 {
		fmt.Println("\nroutes:")
		for _, rt := range p.routes {
			spec := rt.Method + " " + rt.Pattern
			if rt.Group != "" {
				spec += " [" + rt.Group + "]"
			}
			fmt.Printf("  %-40s %-25s %s\n", spec, rt.Name, rt.Pos.String())
		}
	}
	// Traced interfaces are this injector's own declarations, so unlike
	// routes they are listed per spec: which consumers get a wrapper is
	// exactly the question `servo list` exists to answer at a glance.
	if len(p.spec.Traces) > 0 {
		fmt.Println("\ntraced:")
		for _, tr := range p.spec.Traces {
			expected := ""
			if len(tr.Expected) > 0 {
				names := make([]string, len(tr.Expected))
				for i, ex := range tr.Expected {
					names[i] = ex.Obj.Pkg().Name() + "." + ex.Obj.Name()
				}
				expected = "expected: " + strings.Join(names, ", ")
			}
			fmt.Printf("  %-40s %-25s %s\n", tr.Iface.String(), expected, tr.Pos.String())
		}
	}
	return nil
}

// listRejected prints what servo refused, and why: the functions the scan
// rejected, the candidates this injector cannot refer to, and — grouped by
// receiver, since every Init, Stop and Health method in a module lands here
// and on a real project they were most of the output — the methods.
//
// Each line names the type the function would have provided, which is the
// question being asked: "why is nothing providing *cache.Cache?" The JSON
// form keeps one object per entry, methods included, with that type and
// a kind.
func listRejected(p *pipeline, showAll, jsonOut bool) error {
	filtered := filterRejected(p.rejected, p.scope, showAll)
	var pkgs []*packages.Package
	if p.loaded != nil {
		pkgs = p.loaded.All
	}
	unusable := resolve.Unreachable(resolve.Input{
		Spec:       p.spec,
		Candidates: filterCandidates(p.candidates, p.scope, showAll),
		Scope:      p.scope,
		Pkgs:       pkgs,
	})

	if jsonOut {
		type rejectedJSON struct {
			Name     string `json:"name"`
			Pos      string `json:"pos"`
			Reason   string `json:"reason"`
			Provides string `json:"provides,omitempty"`
			Kind     string `json:"kind"`
		}
		out := make([]rejectedJSON, 0, len(filtered)+len(unusable))
		for _, r := range filtered {
			kind := "function"
			if r.IsMethod() {
				kind = "method"
			}
			out = append(out, rejectedJSON{r.Name, r.Pos.String(), r.Reason, shortType(r.Result), kind})
		}
		for _, u := range unusable {
			out = append(out, rejectedJSON{Name: u.Func, Pos: u.Pos.String(), Reason: u.Reason, Provides: providesOf(p.candidates, u), Kind: "unusable"})
		}
		return printJSON(out)
	}

	var funcs, methods []graph.Rejected
	for _, r := range filtered {
		if r.IsMethod() {
			methods = append(methods, r)
		} else {
			funcs = append(funcs, r)
		}
	}
	rows := make([][4]string, 0, len(funcs))
	for _, r := range funcs {
		rows = append(rows, [4]string{r.Name, r.Pos.String(), shortType(r.Result), r.Reason})
	}
	printRows(rows)

	if len(unusable) > 0 {
		if len(rows) > 0 {
			fmt.Println()
		}
		fmt.Printf("candidates %s cannot use:\n", p.spec.InjectorPkg.PkgPath)
		rows = rows[:0]
		for _, u := range unusable {
			rows = append(rows, [4]string{u.Func, u.Pos.String(), providesOf(p.candidates, u), u.Reason})
		}
		printRows(rows)
	}

	if len(methods) > 0 {
		if len(funcs) > 0 || len(unusable) > 0 {
			fmt.Println()
		}
		fmt.Printf("methods are never providers (%d, by receiver):\n", len(methods))
		var order []string
		byRecv := map[string][]string{}
		for _, m := range methods {
			recv, name := splitMethodName(m.Name)
			if _, seen := byRecv[recv]; !seen {
				order = append(order, recv)
			}
			byRecv[recv] = append(byRecv[recv], name)
		}
		width := 0
		for _, recv := range order {
			width = max(width, len(recv))
		}
		for _, recv := range order {
			fmt.Printf("  %-*s  %s\n", width, recv, strings.Join(byRecv[recv], ", "))
		}
	}
	return nil
}

// printRows prints name, position, provided type and reason in columns.
func printRows(rows [][4]string) {
	var w [3]int
	for _, r := range rows {
		for i := range w {
			w[i] = max(w[i], len(r[i]))
		}
	}
	for _, r := range rows {
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", w[0], r[0], w[1], r[1], w[2], r[2], r[3])
	}
}

// shortType renders a rejected function's would-be result by package name,
// *cache.Cache, or "" when there is none.
func shortType(t types.Type) string {
	if t == nil {
		return ""
	}
	return graph.TypeStringWith(t, func(p *types.Package) string { return p.Name() })
}

// providesOf is the type an unusable candidate provides.
func providesOf(candidates []*graph.Provider, u resolve.NearMiss) string {
	for _, c := range candidates {
		if c.Name == u.Func && c.Pos == u.Pos {
			return shortType(c.ResultType)
		}
	}
	return ""
}

// splitMethodName splits "api.(*Server).Run" into its receiver, as the
// group heading, and the method name.
func splitMethodName(name string) (recv, method string) {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return name, ""
	}
	return name[:i], name[i+1:]
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
