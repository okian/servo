// Package resolve implements the selection precedence, cycle detection,
// leveling, and diagnostics that turn a candidate index and a set of
// declared roots into a concrete, ordered construction plan.
package resolve

import (
	"go/token"
	"go/types"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// Node is one resolved value in the object graph, keyed by its provider's
// own result key (its identity — a singleton per graph).
type Node struct {
	Key          graph.Key
	Provider     *graph.Provider
	Deps         []*Node
	Level        int // 1 + max(level(deps)); concurrency level for Init
	Capabilities []string
	// Binding explains why this provider was selected: "explicit bind",
	// "sole candidate", or "sole implementation".
	Binding string
}

// Resolved is the full construction plan.
type Resolved struct {
	Order []*Node // DFS post-order — construction order
	Roots []*Node
	ByKey map[graph.Key]*Node // every resolved node, keyed by its own result key
}

// Input is everything Resolve needs: the spec's roots/binds, the full
// candidate index, capability detection, and the package-path scope that
// structural interface search is restricted to.
//
// ExtraBinds is merged over Spec.Binds with priority, used to build the
// servotest override variant without a second resolver implementation.
type Input struct {
	Spec       *load.Spec
	Candidates []*graph.Provider
	Caps       *graph.Capabilities
	Scope      map[string]bool
	ExtraBinds []load.BindDecl
}

const (
	colorWhite = iota
	colorGray
	colorBlack
)

type resolver struct {
	caps     *graph.Capabilities
	scope    map[string]bool
	all      []*graph.Provider
	byResult map[graph.Key][]*graph.Provider
	explicit map[graph.Key]graph.Key

	nodes       map[graph.Key]*Node
	resolvedKey map[graph.Key]*Node
	failedKey   map[graph.Key]bool
	color       map[graph.Key]int
	order       []*Node
	diags       []Diagnostic
}

// Resolve builds the construction plan for in.Spec.Roots. On any diagnostic
// (missing, ambiguous, or cyclic), Resolved is nil — never a partially
// built plan.
func Resolve(in Input) (*Resolved, []Diagnostic) {
	r := &resolver{
		caps:        in.Caps,
		scope:       in.Scope,
		all:         in.Candidates,
		byResult:    make(map[graph.Key][]*graph.Provider),
		explicit:    make(map[graph.Key]graph.Key),
		nodes:       make(map[graph.Key]*Node),
		resolvedKey: make(map[graph.Key]*Node),
		failedKey:   make(map[graph.Key]bool),
		color:       make(map[graph.Key]int),
	}
	for _, c := range in.Candidates {
		r.byResult[c.Result] = append(r.byResult[c.Result], c)
	}
	for _, b := range in.Spec.Binds {
		r.explicit[b.Iface] = b.Concrete
	}
	for _, b := range in.ExtraBinds {
		r.explicit[b.Iface] = b.Concrete // overrides win over normal binds
	}

	var roots []*Node
	for _, rd := range in.Spec.Roots {
		if node, ok := r.resolveKey(rd.Key, rd.Type, nil, rd.Pos); ok {
			roots = append(roots, node)
		}
	}

	if len(r.diags) > 0 {
		return nil, r.diags
	}
	return &Resolved{Order: r.order, Roots: roots, ByKey: r.nodes}, nil
}

// resolveKey resolves k (statically typed kType). chain is the active path
// of already-resolved consumer frames from the root down to (not
// including) k's own frame; each frame's position is always its own
// provider's declaration. rootPos is threaded unchanged through every
// recursive call and is never a frame's position — it is the Root[]() call
// site this whole traversal descends from, used only to render the
// diagnostic's trailing "root" line (a node can be both a root and, from
// its dependency's point of view, a "needed by" consumer — the diagnostic
// prints both lines for such a node, not one replacing the other).
func (r *resolver) resolveKey(k graph.Key, kType types.Type, chain []chainEntry, rootPos token.Position) (*Node, bool) {
	if n, ok := r.resolvedKey[k]; ok {
		return n, true
	}
	if r.failedKey[k] {
		return nil, false
	}

	sel := r.selectProvider(k, kType)
	if sel.provider == nil {
		r.diags = append(r.diags, r.unresolvedDiagnostic(sel, chain, rootPos))
		r.failedKey[k] = true
		return nil, false
	}

	resultKey := sel.provider.Result
	frame := chainEntry{Key: resultKey, Label: resultKey.String(), Pos: sel.provider.Pos}

	if existing, ok := r.nodes[resultKey]; ok {
		if r.color[resultKey] == colorGray {
			r.diags = append(r.diags, r.cycleDiagnostic(append(chain, frame)))
			r.failedKey[k] = true
			return nil, false
		}
		if r.color[resultKey] == colorBlack {
			r.resolvedKey[k] = existing
			return existing, true
		}
	}

	r.color[resultKey] = colorGray
	node := &Node{Key: resultKey, Provider: sel.provider, Binding: sel.binding, Capabilities: r.caps.Detect(sel.provider.ResultType)}
	r.nodes[resultKey] = node

	newChain := append(append([]chainEntry{}, chain...), frame)
	ok := true
	maxDepLevel := 0
	for i, paramKey := range sel.provider.Params {
		dep, depOK := r.resolveKey(paramKey, sel.provider.ParamTypes[i], newChain, rootPos)
		if !depOK {
			ok = false
			continue
		}
		node.Deps = append(node.Deps, dep)
		if dep.Level > maxDepLevel {
			maxDepLevel = dep.Level
		}
	}
	r.color[resultKey] = colorBlack

	if !ok {
		r.failedKey[k] = true
		return nil, false
	}

	node.Level = maxDepLevel + 1
	r.order = append(r.order, node)
	r.resolvedKey[k] = node
	return node, true
}
