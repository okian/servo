// Package resolve implements the selection precedence, cycle detection,
// leveling, and diagnostics that turn a candidate index and a set of
// declared roots into a concrete, ordered construction plan.
package resolve

import (
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

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

	// Kind separates ordinary provider-built nodes from the synthetic
	// kinds — the two a scope introduces, a supplied value, a config, and
	// a servo.Trace wrapper. Of those only NodeTraced ever appears in
	// Resolved.Order, because it alone is constructed.
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

	// SuppliedType and SuppliedPos are set only on NodeSupplied nodes,
	// which have no Provider to carry a result type or a position.
	SuppliedType types.Type
	SuppliedPos  token.Position

	// Config is set only on NodeConfig nodes: the //servo:config
	// declaration whose generated loader builds this value.
	Config *graph.ConfigDecl

	// Traced is set only on NodeTraced nodes: the servo.Trace declaration
	// the wrapper answers, and the two nodes it is built from.
	Traced *Traced
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

	// HTTP is the emitted server's plan, nil unless the spec declares
	// servo.HTTP(). Its config and dependency nodes are ordinary members
	// of Order; only the routing table lives here.
	HTTP *HTTPPlan
	// Supplied is every servo.Value the graph actually uses, in
	// declaration order. Like the two scope kinds, these never appear in
	// Order — nothing constructs them — so every pre-existing loop over
	// Order stays correct, and an app declaring none emits a
	// byte-identical file.
	Supplied []*Node
	// Configs is every //servo:config type the graph actually uses,
	// ordered by declaration position. Like Supplied they stay out of
	// Order: their loaders run at the top of New, before any provider,
	// and an app using none emits a byte-identical file.
	Configs []*Node
	// Traced is every servo.Trace wrapper the graph actually uses, in
	// declaration order. Unlike the kinds above these ARE in Order — a
	// wrapper is constructed in New, after the implementation and the
	// tracer provider it takes and before its consumers — so they are the
	// one non-provider kind the construction loop has to know about. An
	// app declaring none emits a byte-identical file.
	Traced []*Node
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
	// OverrideVariant marks the resolve that produces NewTestApp. An
	// Override may legitimately remove the only consumer of a declared
	// Value, Trace or ConfigFile — a mock taking fewer things than the real
	// implementation is the ordinary case — so in this variant an unused
	// declaration is left out of the generated file rather than refused.
	// The production resolve, where the spec file is the whole truth about
	// the graph, still refuses it.
	OverrideVariant bool
	// Configs is every //servo:config declaration in the main module. A
	// requested type matching one resolves to its generated loader; the
	// ones nothing requests are simply not part of this graph.
	Configs []*graph.ConfigDecl
	// Fset and Pkgs are used only by scope detection: positions for a
	// ScopeKey method, and its declaration's syntax for the blank-receiver
	// check. Both are optional — a graph with no Scoped declarations never
	// looks at them.
	Fset *token.FileSet
	Pkgs []*packages.Package

	// HTTP is non-nil when the spec declares servo.HTTP(): the module's
	// scanned routes plus the config key the emitted server requires. Nil
	// leaves resolution byte-for-byte as before.
	HTTP *HTTPInput

	// Trace is the trace.TracerProvider key every servo.Trace wrapper
	// depends on, nil when the OpenTelemetry trace API is not in the
	// module's build. Read only when the spec declares a Trace.
	Trace *TraceInput
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
	suppliedByKey  map[graph.Key]*Node
	suppliedUsed   map[graph.Key]bool
	declaredScope  map[graph.Key]bool // scoped types with a servo.Scoped declaration
	configByKey    map[graph.Key]*graph.ConfigDecl
	configNodes    []*Node
	activeScope    *Scope
	traceIn        *TraceInput
	traceByKey     map[graph.Key]*load.TraceDecl
	traceUsed      map[graph.Key]bool
	traceNodes     []*Node
	// overrideVariant is Input.OverrideVariant: the unused-declaration
	// checks step aside, and Resolved.Supplied carries only the used values.
	overrideVariant bool
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
		caps:            in.Caps,
		scope:           in.Scope,
		all:             in.Candidates,
		byResult:        make(map[graph.Key][]*graph.Provider),
		explicit:        make(map[graph.Key]graph.Key),
		nodes:           make(map[graph.Key]*Node),
		resolvedKey:     make(map[graph.Key]*Node),
		failedKey:       make(map[graph.Key]bool),
		color:           make(map[graph.Key]int),
		fset:            fset,
		pkgs:            in.Pkgs,
		rootPos:         make(map[*Node]token.Position),
		specRootPos:     make(map[*Node]token.Position),
		specRootDecl:    make(map[*Node]graph.Key),
		explicitPos:     make(map[graph.Key]token.Position),
		scopeByKey:      make(map[graph.Key]*Scope),
		accessorByKey:   make(map[graph.Key]*ScopeRoot),
		suppliedByKey:   make(map[graph.Key]*Node),
		suppliedUsed:    make(map[graph.Key]bool),
		declaredScope:   make(map[graph.Key]bool),
		configByKey:     make(map[graph.Key]*graph.ConfigDecl),
		traceByKey:      make(map[graph.Key]*load.TraceDecl),
		traceUsed:       make(map[graph.Key]bool),
		overrideVariant: in.OverrideVariant,
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
	// Built before anything resolves, so a supplied type is already
	// available the first time any consumer asks for it — including from
	// inside a scope's sub-graph, where it is borrowed exactly as a
	// singleton is.
	for _, v := range in.Spec.Values {
		r.suppliedByKey[v.Key] = &Node{Key: v.Key, Kind: NodeSupplied, Level: 0, SuppliedType: v.Type, SuppliedPos: v.Pos}
	}
	for _, d := range in.Configs {
		r.configByKey[d.Key] = d
		// A hand-written constructor for a //servo:config type would never
		// be selected — the directive short-circuits ahead of provider
		// selection, exactly as a declared scope accessor does — so left
		// alone it would sit in the code looking authoritative while the
		// generated loader quietly wins. Said now, at the declaration,
		// rather than discovered in production.
		for _, c := range r.byResult[d.Key] {
			r.diags = append(r.diags, r.configProviderDiagnostic(d, c))
		}
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
	// After the accessors are known, so a Trace naming one is refused by
	// name; before any traversal, so the first request for a traced
	// interface already finds its declaration.
	r.setupTraces(in)
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

	// After the roots so shared subtrees keep their root-attributed
	// positions, before checkScopeEdges so nodes only HTTP reaches are in
	// r.order for the widening walk.
	httpPlan := r.resolveHTTP(in.HTTP)

	if len(r.diags) > 0 {
		return nil, r.diags
	}
	// Before the scope-edge walk: a wrapper around a scoped instance is
	// refused here with its own message, and the generic widening
	// diagnostic for the same edge would only say it less well.
	r.checkTraces(in.Spec.Traces)
	r.checkScopeEdges(roots)
	r.checkAccessorInterfaces()
	r.checkSuppliedValues(in.Spec.Values)
	r.checkConfigs(in.Spec)
	if len(r.diags) > 0 {
		return nil, r.diags
	}

	var supplied []*Node
	for _, v := range in.Spec.Values {
		// In the override variant a value the overridden graph no longer
		// reads is left off TestValues rather than refused — see
		// checkSuppliedValues — so only the used ones are handed on.
		if r.overrideVariant && !r.suppliedUsed[v.Key] {
			continue
		}
		supplied = append(supplied, r.suppliedByKey[v.Key])
	}
	// Declaration-position order, not discovery order: which root's DFS
	// reached a config first is an accident of the spec file's line order,
	// and the generated preamble should not reshuffle when roots do.
	sort.Slice(r.configNodes, func(i, j int) bool {
		return graph.ComparePos(r.configNodes[i].Config.Pos, r.configNodes[j].Config.Pos) < 0
	})
	return &Resolved{Order: r.finishScopes(), Roots: roots, ByKey: r.nodes, Scopes: r.scopes, HTTP: httpPlan, Supplied: supplied, Configs: r.configNodes, Traced: sortedTraceNodes(r.traceNodes)}, nil
}

// checkConfigs runs the config checks that only make sense once the graph
// is resolved: a scoped member taking a config directly, two used configs
// resolving a setting to the same environment variable (or, with a config
// file declared, the same section key), and a servo.ConfigFile declaration
// no config in the graph would ever read.
func (r *resolver) checkConfigs(spec *load.Spec) {
	// A config value is a local in New, never an App field, so a scope's
	// per-key constructions — which read every borrowed singleton off the
	// App — cannot take one directly. A singleton *between* them is fine
	// (it is built by New, where the value is in scope), and is exactly
	// the workaround the diagnostic names. Checked here, after membership
	// is settled, rather than during traversal: the scope pass resolves
	// borrowed singletons' whole sub-graphs with the scope active, and a
	// config two constructors deep is legal. Members still sit in r.order
	// at this point — finishScopes moves them out later — so membership is
	// read off each node's own Scope.
	for _, n := range r.order {
		if n.Scope == nil {
			continue
		}
		for _, d := range n.Deps {
			if d.Kind == NodeConfig {
				r.diags = append(r.diags, r.configInScopeDiagnostic(d.Config, n, n.Scope.Pos))
			}
		}
	}
	type claim struct {
		decl  *graph.ConfigDecl
		field graph.ConfigField
	}
	envClaims := map[string]claim{}
	fileClaims := map[string]claim{}
	for _, n := range sortedConfigNodes(r.configNodes) {
		for _, f := range n.Config.Fields {
			if prior, dup := envClaims[f.EnvName]; dup {
				r.diags = append(r.diags, r.configCollisionDiagnostic("environment variable", f.EnvName, prior.decl, prior.field, n.Config, f))
			} else {
				envClaims[f.EnvName] = claim{n.Config, f}
			}
			if spec.ConfigFile == nil {
				continue
			}
			fileKey := n.Config.Section + "." + f.FileKey
			if prior, dup := fileClaims[fileKey]; dup {
				r.diags = append(r.diags, r.configCollisionDiagnostic("config file key", fileKey, prior.decl, prior.field, n.Config, f))
			} else {
				fileClaims[fileKey] = claim{n.Config, f}
			}
		}
	}
	// Not in the override variant: a mock may have replaced the only
	// consumer of a config, and the test variant then simply reads no file
	// — the same leniency an unused Value gets there.
	if spec.ConfigFile != nil && len(r.configNodes) == 0 && !r.overrideVariant {
		r.diags = append(r.diags, r.unusedConfigFileDiagnostic(spec.ConfigFile))
	}
}

// sortedConfigNodes orders by declaration position without mutating the
// resolver's own slice, so collision reporting is deterministic even when
// it runs before the final sort in Resolve.
func sortedConfigNodes(nodes []*Node) []*Node {
	sorted := append([]*Node(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool {
		return graph.ComparePos(sorted[i].Config.Pos, sorted[j].Config.Pos) < 0
	})
	return sorted
}

// checkSuppliedValues reports a servo.Value nothing depends on.
//
// Unused, it would still add a field to the generated Values struct, so
// every caller would keep passing a value the app never reads — the kind
// of thing that is true for a release and then quietly wrong. Saying so is
// consistent with how servo treats the rest of the spec: an unresolvable
// declaration is a build failure, not a warning.
func (r *resolver) checkSuppliedValues(values []load.ValueDecl) {
	// The override variant is the one place an unused declaration is not
	// the spec file's mistake: an Override swaps in a mock, and a mock that
	// takes fewer things than the real implementation is the ordinary case.
	// The value is left off TestValues instead (see Resolve), so the test
	// API describes what the test graph actually reads.
	if r.overrideVariant {
		return
	}
	for _, v := range values {
		if r.suppliedUsed[v.Key] {
			continue
		}
		r.diags = append(r.diags, r.unusedValueDiagnostic(v))
	}
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
	// A traced interface answers every request with its wrapper, whatever
	// the interface itself would have resolved to — which is what the
	// wrapper resolves next, through the ordinary path below.
	if decl, traced := r.traceByKey[k]; traced {
		return r.resolveTraced(decl, k, kType, chain, rootPos)
	}
	return r.resolveUntraced(k, kType, chain, rootPos)
}

// resolveUntraced is resolveKey without the trace indirection: the
// precedence walk itself. resolveTraced calls it for the implementation
// behind a wrapper; everything else goes through resolveKey.
func (r *resolver) resolveUntraced(k graph.Key, kType types.Type, chain []chainEntry, rootPos token.Position) (*Node, bool) {
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
	// And so does a servo.Value, for the same reason and ahead of any
	// provider that also produces the type: declaring one is how you say
	// "this comes from the caller", which is only meaningful if it wins.
	if n, ok := r.suppliedByKey[k]; ok {
		r.suppliedUsed[k] = true
		return n, true
	}

	if n, ok := r.resolvedKey[k]; ok {
		return n, true
	}
	if r.failedKey[k] {
		return nil, false
	}

	// A //servo:config type resolves to its generated loader, ahead of
	// provider selection for the same reason a declared accessor does: the
	// directive said so. Resolving one from inside a scope's sub-graph is
	// fine as long as the consumer is a borrowed singleton (constructed by
	// New, where the loaded value is in scope as a local); a scoped
	// *member* taking one directly is refused later, in checkConfigs, once
	// membership is known.
	if decl, ok := r.configByKey[k]; ok {
		node := &Node{Key: k, Kind: NodeConfig, Level: 0, Config: decl, Binding: "config directive"}
		r.nodes[k] = node
		r.resolvedKey[k] = node
		r.configNodes = append(r.configNodes, node)
		return node, true
	}

	sel := r.selectProvider(k, kType)
	if sel.provider == nil {
		// The tracer provider is the one dependency the user did not ask
		// for by name, so "no provider, zero implementations" needs to
		// say whose job writing one is.
		if r.traceIn != nil && k == r.traceIn.ProviderKey && len(sel.candidates) == 0 {
			r.diags = append(r.diags, r.missingTracerProviderDiagnostic(chain, rootPos))
		} else {
			r.diags = append(r.diags, r.unresolvedDiagnostic(sel, chain, rootPos))
		}
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

	if !r.checkScopeDeclaration(node, sel.provider, append(append([]chainEntry{}, chain...), frame), rootPos) {
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

// checkScopeDeclaration reports a type carrying a ScopeKey method that no
// servo.Scoped declaration names. It is one half of a pair, the other
// being missingScopeKeyDiagnostic: a declaration whose type has no method.
// It runs at selection time rather than afterwards so the message is the
// one the user needs, instead of the "no provider for chat.RoomKey" that
// descending into a scoped constructor's key parameter would otherwise
// produce.
func (r *resolver) checkScopeDeclaration(node *Node, p *graph.Provider, chain []chainEntry, rootPos token.Position) bool {
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
	// leading context. Anything narrower would let the most dangerous
	// shape through — an extractor that forgot its error result, which
	// silently gives every keyless caller the zero key. Anything wider
	// would catch ordinary methods that merely share the name.
	if !graph.ScopeKeyLikely(p.ResultType) {
		return true
	}
	// Beyond that the extractor is recognized, not validated. Whatever
	// else is wrong with it is downstream of the thing the user has to do
	// first, which is decide whether this type is scoped at all;
	// buildScopes reports the rest, strictly, once they have said it is.
	r.diags = append(r.diags, r.undeclaredScopeDiagnostic(node, graph.ScopeKeyPos(r.fset, p.ResultType), chain, rootPos))
	return false
}
