// Package resolve implements the selection precedence, cycle detection,
// leveling, and diagnostics that turn a candidate index and a set of
// declared roots into a concrete, ordered construction plan.
package resolve

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

var errorType = types.Universe.Lookup("error").Type()

// Node is one resolved value in the object graph, keyed by its provider's
// own result key (its identity — a singleton per graph, or one per live
// key within a scope).
type Node struct {
	Key          graph.Key
	Provider     *graph.Provider
	Deps         []*Node
	Level        int // 1 + max(level(deps)); concurrency level for Init
	Capabilities []string
	// Binding explains why this provider was selected: "explicit bind",
	// "sole candidate", or "sole implementation".
	Binding string

	// Kind separates ordinary provider-built nodes from the two synthetic
	// kinds a scope introduces. Only NodeProvider nodes ever appear in
	// Resolved.Order or Scope.Order.
	Kind NodeKind
	// Scope is the scope this node belongs to, or nil for a singleton.
	// Every node in the graph is one or the other; a node that a scope
	// merely borrows (a logger, a config) stays a singleton.
	Scope *Scope
	// ScopeRoot is set only on NodeScopeAccessor nodes.
	ScopeRoot *ScopeRoot
	// ScopeLevel is the node's concurrency level within its own scope,
	// counted from the scope's own floor rather than the app's, so a
	// scope's Init phases don't depend on how deep the singletons it
	// borrows happen to be.
	ScopeLevel int
}

// Scoped reports whether this node is one instance per key rather than one
// per process.
func (n *Node) Scoped() bool { return n.Scope != nil }

// Resolved is the full construction plan.
type Resolved struct {
	Order  []*Node // DFS post-order — construction order, singletons only
	Roots  []*Node
	ByKey  map[graph.Key]*Node // every resolved node, keyed by its own result key
	Scopes []*Scope            // declared scopes, ordered by key type
}

// Input is everything Resolve needs: the spec's roots/binds/scopes, the
// full candidate index, capability detection, and the package-path scope
// that structural interface search is restricted to.
//
// ExtraBinds is merged over Spec.Binds with priority, used to build the
// servotest override variant without a second resolver implementation.
type Input struct {
	Spec       *load.Spec
	Candidates []*graph.Provider
	Caps       *graph.Capabilities
	Scope      map[string]bool
	ExtraBinds []load.BindDecl
	// Fset and Pkgs are used only by scope detection: positions for a
	// ScopeKey method, and its declaration's syntax for the blank-receiver
	// check. Both are optional — a graph with no Scoped declarations never
	// looks at them.
	Fset *token.FileSet
	Pkgs []*packages.Package
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

	fset           *token.FileSet
	pkgs           []*packages.Package
	scopes         []*Scope
	claimConflicts []claimConflict
	rootPos        map[*Node]token.Position
	specRootPos    map[*Node]token.Position
	specRootDecl   map[*Node]graph.Key
	explicitPos    map[graph.Key]token.Position
	scopeByKey     map[graph.Key]*Scope
	accessorByKey  map[graph.Key]*ScopeRoot
	declaredScope  map[graph.Key]bool // scoped types with a servo.Scoped declaration
	activeScope    *Scope
}

// Resolve builds the construction plan for in.Spec.Roots. On any
// diagnostic (missing, ambiguous, cyclic, or any of the scope
// diagnostics), Resolved is nil — never a partially built plan.
//
// Scopes are resolved before roots, deliberately. Membership has to be
// known before the app's own traversal runs, because the check that gives
// this feature its reason to exist — a singleton capturing a scoped
// instance — is a question about a node the scope pass already classified.
func Resolve(in Input) (*Resolved, []Diagnostic) {
	fset := in.Fset
	if fset == nil {
		fset = token.NewFileSet()
	}
	r := &resolver{
		caps:          in.Caps,
		scope:         in.Scope,
		all:           in.Candidates,
		byResult:      make(map[graph.Key][]*graph.Provider),
		explicit:      make(map[graph.Key]graph.Key),
		nodes:         make(map[graph.Key]*Node),
		resolvedKey:   make(map[graph.Key]*Node),
		failedKey:     make(map[graph.Key]bool),
		color:         make(map[graph.Key]int),
		fset:          fset,
		pkgs:          in.Pkgs,
		rootPos:       make(map[*Node]token.Position),
		specRootPos:   make(map[*Node]token.Position),
		specRootDecl:  make(map[*Node]graph.Key),
		explicitPos:   make(map[graph.Key]token.Position),
		scopeByKey:    make(map[graph.Key]*Scope),
		accessorByKey: make(map[graph.Key]*ScopeRoot),
		declaredScope: make(map[graph.Key]bool),
	}
	for _, c := range in.Candidates {
		r.byResult[c.Result] = append(r.byResult[c.Result], c)
	}
	for _, b := range in.Spec.Binds {
		r.explicit[b.Iface] = b.Concrete
		r.explicitPos[b.Iface] = b.Pos
	}
	for _, b := range in.ExtraBinds {
		r.explicit[b.Iface] = b.Concrete // overrides win over normal binds
		r.explicitPos[b.Iface] = b.Pos
	}
	for _, d := range in.Spec.Scopes {
		r.declaredScope[d.Impl] = true
	}

	r.buildScopes(in.Spec.Scopes)
	if len(r.diags) > 0 {
		return nil, r.diags
	}
	for _, s := range r.scopes {
		for _, root := range s.Roots {
			r.accessorByKey[root.Iface] = root
			// A declared accessor interface resolves to generated code, not
			// to a candidate — so it never reaches selectProvider, which is
			// the only place Bind and Override are consulted. Left alone,
			// an Override naming it would be silently ignored and a test
			// App would quietly exercise the real scope.
			if concrete, bound := r.explicit[root.Iface]; bound {
				r.diags = append(r.diags, r.boundAccessorDiagnostic(root, concrete))
			}
			// And the same mistake made with a constructor instead of a
			// marker: a function returning the accessor interface is an
			// accepted candidate that resolution will never select,
			// because the accessor short-circuits ahead of it.
			for _, c := range r.byResult[root.Iface] {
				r.diags = append(r.diags, r.accessorProviderDiagnostic(root, c))
			}
		}
	}
	if len(r.diags) > 0 {
		return nil, r.diags
	}
	r.resolveScopes(in.Spec.Scopes)

	var roots []*Node
	for _, rd := range in.Spec.Roots {
		if node, ok := r.resolveKey(rd.Key, rd.Type, nil, rd.Pos); ok {
			roots = append(roots, node)
			if _, seen := r.rootPos[node]; !seen {
				r.rootPos[node] = rd.Pos
			}
			// Kept separately from rootPos, which a scope root already
			// claimed with its servo.Scoped position: a diagnostic that
			// says "delete this servo.Root" has to point at the Root.
			if _, seen := r.specRootPos[node]; !seen {
				r.specRootPos[node] = rd.Pos
				r.specRootDecl[node] = rd.Key
			}
		}
	}

	if len(r.diags) > 0 {
		return nil, r.diags
	}
	r.checkScopeEdges(roots)
	r.checkAccessorInterfaces()
	if len(r.diags) > 0 {
		return nil, r.diags
	}

	return &Resolved{Order: r.finishScopes(), Roots: roots, ByKey: r.nodes, Scopes: r.scopes}, nil
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
	// A scope's key is produced per instance by its extractor, so it
	// resolves only while some scope's sub-graph is being walked, and is
	// deliberately never cached in resolvedKey: the identical key
	// requested from the app's traversal is a different question with a
	// different answer.
	//
	// Any declared scope's key resolves here, not just the active one.
	// Reaching another scope's key from inside this one is a nested scope,
	// which is rejected — but it is rejected by checkScopeEdges, with both
	// key types named and a chain to point at. Refusing to resolve it here
	// would instead produce "no provider for TenantKey", which describes
	// the symptom and not the mistake.
	if s, ok := r.scopeByKey[k]; ok {
		if r.activeScope != nil {
			return s.Key, true
		}
		r.diags = append(r.diags, r.scopeKeyOutsideScopeDiagnostic(s, chain, rootPos))
		r.failedKey[k] = true
		return nil, false
	}
	// A declared accessor interface always beats structural search, for
	// the same reason an explicit Bind does: the spec file said so.
	if root, ok := r.accessorByKey[k]; ok {
		return root.Accessor, true
	}

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

	if !r.checkScopeDeclaration(node, sel.provider) {
		r.color[resultKey] = colorBlack
		r.failedKey[k] = true
		return nil, false
	}

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

// checkScopeDeclaration is the "undeclared scope" half of §8's pair: a
// type carrying a ScopeKey method that no servo.Scoped declaration names.
// It runs at selection time rather than afterwards so the message is the
// one the user needs, instead of the "no provider for chat.RoomKey" that
// descending into a scoped constructor's key parameter would otherwise
// produce.
func (r *resolver) checkScopeDeclaration(node *Node, p *graph.Provider) bool {
	if r.declaredScope[node.Key] {
		return true // buildScopes already validated it, strictly
	}
	// `ScopeKey` is an ordinary method name. Asking whether an undeclared
	// type is scoped must not impose the extractor's full signature on
	// every method that happens to share the name — a module with no
	// scopes at all would start failing to generate because something in
	// its dependency tree has a ScopeKey() string.
	// Only types the user can actually change. A dependency whose own
	// types happen to have a method of this name is not something they
	// can declare a scope for or delete the method from, and failing
	// their build over it would be hostile — "zero impact on apps that
	// declare no scopes" has to survive contact with other people's code.
	if !r.scope[p.Pkg] {
		return true
	}
	// The gate is `ScopeKey(ctx context.Context, ...)`: the name plus a
	// leading context. Anything narrower would let the one shape the PRD
	// calls out as most dangerous through — an extractor that forgot its
	// error result, which silently gives every keyless caller the zero
	// key. Anything wider would catch ordinary methods that merely share
	// the name.
	if !graph.ScopeKeyLikely(p.ResultType) {
		return true
	}
	// Beyond that the extractor is recognized, not validated. Whatever
	// else is wrong with it is downstream of the thing the user has to do
	// first, which is decide whether this type is scoped at all;
	// buildScopes reports the rest, strictly, once they have said it is.
	r.diags = append(r.diags, r.undeclaredScopeDiagnostic(node, graph.ScopeKeyPos(r.fset, p.ResultType)))
	return false
}
