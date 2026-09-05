package resolve

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/route"
)

// HTTPInput is the resolver's slice of a servo.HTTP() declaration: the
// marker's position, the module's scanned routes, and the key for the
// *servo.HTTPConfig node the emitted server reads.
type HTTPInput struct {
	Pos        token.Position
	Routes     []*route.Route
	ConfigKey  graph.Key
	ConfigType types.Type
}

// HTTPPlan is the resolved half of the emitted HTTP server: the config
// node and, per route, the nodes backing the handler's dependencies. The
// server itself is emitted machinery, not a node — its requirements enter
// the graph here as root-like entry points.
type HTTPPlan struct {
	Pos    token.Position
	Config *Node
	Routes []*HTTPRoute
}

// HTTPRoute pairs one scanned route with its resolved dependency nodes,
// parallel to Route.Deps.
type HTTPRoute struct {
	Route *route.Route
	Deps  []*Node
}

// resolveHTTP resolves the config key and every handler dependency through
// the ordinary resolveKey recursion, so unresolved keys render with the
// same needed-by chains as any root's — the only HTTP-specific rule is
// that a handler dependency must be a singleton, checked here because the
// consumer (the emitted server) is not a node checkScopeEdges could see.
func (r *resolver) resolveHTTP(in *HTTPInput) *HTTPPlan {
	if in == nil {
		return nil
	}
	plan := &HTTPPlan{Pos: in.Pos}

	chain := []chainEntry{{Key: in.ConfigKey, Label: "HTTP server (servo.HTTP())", Pos: in.Pos}}
	if node, ok := r.resolveKey(in.ConfigKey, in.ConfigType, chain, in.Pos); ok {
		plan.Config = node
		if _, seen := r.rootPos[node]; !seen {
			r.rootPos[node] = in.Pos
		}
	}

	for _, rt := range in.Routes {
		hr := &HTTPRoute{Route: rt}
		label := fmt.Sprintf("handler %s (%s %s)", rt.Name, rt.Method, rt.Pattern)
		rchain := []chainEntry{{Label: label, Pos: rt.Pos}}
		for i, depKey := range rt.Deps {
			before := len(r.diags)
			node, ok := r.resolveKey(depKey, rt.DepTypes[i], rchain, in.Pos)
			if !ok {
				r.maybeRequestStructHint(before, rt, i)
				continue
			}
			if node.Scoped() && node.Kind == NodeProvider {
				r.diags = append(r.diags, r.scopedHandlerDepDiagnostic(rt, node))
				continue
			}
			hr.Deps = append(hr.Deps, node)
			if _, seen := r.rootPos[node]; !seen {
				r.rootPos[node] = rt.Pos
			}
		}
		plan.Routes = append(plan.Routes, hr)
	}
	return plan
}

// maybeRequestStructHint extends the diagnostic a failed dependency just
// produced when the shape suggests the real mistake was elsewhere: the
// handler's second parameter is a pointer to a struct with no binding
// tags, so the scanner classified it as a dependency, and "no provider"
// alone sends the user hunting for a constructor they never meant to
// write. Appended only to a diagnostic added by this very call — a
// dependency that failed earlier (failedKey) was already hinted once.
func (r *resolver) maybeRequestStructHint(before int, rt *route.Route, depIdx int) {
	if len(r.diags) == before {
		return
	}
	if rt.Req != nil || depIdx != 0 {
		return // not in the request-struct position
	}
	ptr, ok := types.Unalias(rt.DepTypes[depIdx]).(*types.Pointer)
	if !ok {
		return
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	if !ok {
		return
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return
	}
	last := &r.diags[len(r.diags)-1]
	last.Message += fmt.Sprintf("\n  If %s was meant as the request struct rather than a dependency, give its\n  fields binding tags (path/query/header/form/json) — only the second\n  parameter can be the request struct, and the tags are what mark it as one.\n", graph.TypeString(named))
}

// scopedHandlerDepDiagnostic is the widening rule with the emitted HTTP
// server as the capturing singleton: the server's dependencies are
// resolved once at construction, so a scoped parameter would pin one key's
// instance for every request.
func (r *resolver) scopedHandlerDepDiagnostic(rt *route.Route, scoped *Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is scoped, but handler %s (%s %s) receives it from the HTTP server, a singleton that would capture it\n\n",
		scoped.Key.String(), rt.Name, rt.Method, rt.Pattern)
	fmt.Fprintf(&b, "  The server resolves a handler's dependencies once, at construction, so every\n")
	fmt.Fprintf(&b, "  request would share whichever %s happened to be built first,\n", scoped.Key.String())
	fmt.Fprintf(&b, "  whatever key the request presents. Nothing about the running program would say so.\n\n")
	if iface := r.accessorIfaceFor(scoped); iface != "" {
		fmt.Fprintf(&b, "  Depend on the accessor instead: change the handler's parameter from %s\n  to %s, and call Acquire(ctx) per request.\n", scoped.Key.String(), iface)
	} else {
		fmt.Fprintf(&b, "  Depend on the scope's accessor interface instead of on %s directly,\n  and call Acquire(ctx) per request.\n", scoped.Key.String())
	}
	return Diagnostic{Pos: rt.Pos, Message: b.String()}
}
