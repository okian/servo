package resolve

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"time"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// Scope is one keyed sub-graph: everything reachable from a servo.Scoped
// declaration that depends on that declaration's key type, plus the policy
// and accessors the generator needs to emit a registry for it.
//
// Scope identity is the key type's identity. Two servo.Scoped declarations
// whose ScopeKey methods return the same defined type are one scope, with
// one instance map, one reference count and one linger timer per key —
// which is what makes a room and its room-scoped log share a lifetime
// rather than accidentally getting two.
type Scope struct {
	KeyType types.Type
	KeyKey  graph.Key
	// Name is the scope's stable identifier in reports and diagnostics,
	// derived from the key type so it survives renames of the scoped type.
	Name   string
	Linger time.Duration
	Max    int

	// Order is every scoped member, in construction order. Shared
	// singletons the scope also depends on are not here: they live in
	// Resolved.Order and are constructed once, by the App.
	Order []*Node
	Roots []*ScopeRoot
	// Key is the synthetic node standing for the extracted key value. It
	// appears only in other nodes' Deps, never in any Order.
	Key *Node
	Pos token.Position
}

// ScopeRoot is one servo.Scoped declaration inside a scope: the type an
// accessor hands out, the user-declared interface that accessor satisfies,
// and the extractor to call to turn a context into a key.
type ScopeRoot struct {
	Node      *Node
	Iface     graph.Key
	IfaceType types.Type
	Extractor *graph.ScopeKey
	// ExtractorDeps are the extractor's own dependencies, resolved as
	// ordinary singletons — the extractor runs before any instance
	// exists, so it cannot depend on one.
	ExtractorDeps []*Node
	Accessor      *Node
	Pos           token.Position
}

// NodeKind separates the five things a *Node can stand for. All but
// NodeProvider exist only as entries in another node's Deps: they are
// never constructed by a provider function and never appear in an Order
// (supplied values live in Resolved.Supplied, configs in Resolved.Configs).
type NodeKind int

const (
	// NodeProvider is an ordinary value built by a constructor.
	NodeProvider NodeKind = iota
	// NodeScopeKey is a scope's key value, supplied per instance by the
	// extractor at acquire time rather than by any provider.
	NodeScopeKey
	// NodeScopeAccessor is the generated accessor for a declared scope,
	// injected wherever that scope's interface is requested.
	NodeScopeAccessor
	// NodeSupplied is a servo.Value: handed to the generated NewWith by
	// the caller, once per app, rather than built from a provider. It has
	// no provider, no dependencies and no lifecycle — servo did not make
	// it, so servo does not stop it.
	NodeSupplied
	// NodeConfig is a //servo:config type: built by the generated
	// ServoConfig loader in its own package, at the very top of New,
	// before any provider runs. Like NodeSupplied it never appears in
	// Resolved.Order — it lives in Resolved.Configs instead — and it has
	// no lifecycle: a config is data, and anything with something to
	// start or stop is a component that should receive the config, not
	// be one.
	NodeConfig
)

// scopeName renders a scope's stable identifier.
func scopeName(key graph.Key) string { return "scope " + key.String() }

// buildScopes groups the spec's Scoped declarations by key type and
// validates each declaration in isolation — that the scoped type has a
// provider at all, that it declares ScopeKey, that the extractor's
// receiver cannot be dereferenced, and that two declarations sharing a key
// type agree on that scope's policy.
func (r *resolver) buildScopes(decls []load.ScopeDecl) {
	type group struct {
		scope *Scope
		// Whoever set Linger/Max first, so a conflicting second
		// declaration can be reported against a position rather than
		// against "somewhere else in this file".
		lingerFrom, maxFrom *load.ScopeDecl
	}
	groups := map[graph.Key]*group{}
	var order []graph.Key

	for i := range decls {
		d := decls[i]
		sk, err := graph.FindScopeKey(r.fset, d.ImplType)
		if err != nil {
			r.diags = append(r.diags, scopeKeyDiagnostic(err))
			continue
		}
		if sk == nil {
			r.diags = append(r.diags, r.missingScopeKeyDiagnostic(d))
			continue
		}
		if !r.extractorReceiverIsBlank(sk) {
			r.diags = append(r.diags, r.namedReceiverDiagnostic(sk))
			continue
		}
		if _, isPtr := types.Unalias(d.ImplType).(*types.Pointer); !isPtr {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
				"servo: servo.Scoped's first type argument must be a pointer, not %s\n\n  Acquire has to be able to report failure, and its instance result is %s — a\n  value type has no zero it can return alongside an error. Declare\n  servo.Scoped[*%s, %s] and return *%s from the constructor.\n",
				d.Impl.String(), d.Impl.String(), localTypeString(d.ImplType), d.Iface.String(), localTypeString(d.ImplType))})
			continue
		}

		g, ok := groups[sk.KeyKey]
		if !ok {
			g = &group{scope: &Scope{
				KeyType: sk.KeyType, KeyKey: sk.KeyKey, Name: scopeName(sk.KeyKey),
				Linger: d.EffectiveLinger(), Max: d.EffectiveMax(), Pos: d.Pos,
			}}
			groups[sk.KeyKey] = g
			order = append(order, sk.KeyKey)
			if d.LingerSet {
				g.lingerFrom = &decls[i]
			}
			if d.MaxSet {
				g.maxFrom = &decls[i]
			}
		} else {
			r.mergeScopePolicy(g.scope, &decls[i], &g.lingerFrom, &g.maxFrom)
		}

		g.scope.Roots = append(g.scope.Roots, &ScopeRoot{
			Iface: d.Iface, IfaceType: d.IfaceType, Extractor: sk, Pos: d.Pos,
		})
	}

	// Sorted by key type so a module with several scopes emits the same
	// file regardless of the order declarations happen to appear in.
	sort.Slice(order, func(i, j int) bool { return order[i].String() < order[j].String() })
	for _, k := range order {
		s := groups[k].scope
		s.Key = &Node{Key: s.KeyKey, Kind: NodeScopeKey, Scope: s, Level: 0}
		// Accessor nodes are created here, before any scope's sub-graph is
		// walked, rather than alongside the root they expose. A member of
		// one scope may legitimately depend on another scope's accessor —
		// that is the documented way out of the cross-scope rejection —
		// and creating them lazily would leave the later scope's accessor
		// nil at exactly the moment the earlier scope asked for it.
		for _, root := range s.Roots {
			root.Accessor = &Node{Key: root.Iface, Kind: NodeScopeAccessor, Scope: s, ScopeRoot: root, Level: 0}
		}
		r.scopes = append(r.scopes, s)
		r.scopeByKey[k] = s
	}
}

// mergeScopePolicy folds a second declaration's options into the scope the
// first one created. Two declarations sharing a key type share one
// registry, so they cannot disagree about how long it lingers or how large
// it may grow — there is only one of each to configure.
func (r *resolver) mergeScopePolicy(s *Scope, d *load.ScopeDecl, lingerFrom, maxFrom **load.ScopeDecl) {
	if d.LingerSet {
		if *lingerFrom != nil && (*lingerFrom).Linger != d.Linger {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
				"servo: conflicting servo.Linger for scope %s\n  %s declares %s  %s\n  %s declares %s  %s\n\n  Both types key their scope on %s, so they share one registry, one reference count and one linger timer — there is only one window to set.\n",
				s.KeyKey.String(), (*lingerFrom).Impl.String(), (*lingerFrom).Linger, (*lingerFrom).Pos,
				d.Impl.String(), d.Linger, d.Pos, s.KeyKey.String())})
		} else {
			s.Linger = d.Linger
			*lingerFrom = d
		}
	}
	if d.MaxSet {
		if *maxFrom != nil && (*maxFrom).Max != d.Max {
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
				"servo: conflicting servo.Max for scope %s\n  %s declares %d  %s\n  %s declares %d  %s\n\n  Both types key their scope on %s, so they share one instance map — there is only one cap to set.\n",
				s.KeyKey.String(), (*maxFrom).Impl.String(), (*maxFrom).Max, (*maxFrom).Pos,
				d.Impl.String(), d.Max, d.Pos, s.KeyKey.String())})
		} else {
			s.Max = d.Max
			*maxFrom = d
		}
	}
}

// extractorReceiverIsBlank reports whether the extractor can be safely
// called on a typed nil. Generated code has to: it needs the key before it
// can choose an instance, so there is no instance to call it on. When the
// declaring package was loaded without syntax there is nothing to inspect,
// and the check passes rather than blocking generation on a
// load-mode detail — servo-vet runs the identical check in the editor.
func (r *resolver) extractorReceiverIsBlank(sk *graph.ScopeKey) bool {
	decl := graph.FuncDeclOf(r.pkgs, sk.Func)
	if decl == nil {
		return true
	}
	return graph.ReceiverIsBlank(decl)
}

// resolveScopes resolves every scope's sub-graph, then works out which of
// the nodes it reached actually belong to the scope.
func (r *resolver) resolveScopes(decls []load.ScopeDecl) {
	byImpl := map[graph.Key]load.ScopeDecl{}
	for _, d := range decls {
		byImpl[d.Impl] = d
	}

	for _, s := range r.scopes {
		r.activeScope = s
		for _, root := range s.Roots {
			d := byImpl[root.Extractor.Owner]
			node, ok := r.resolveKey(root.Extractor.Owner, root.Extractor.OwnerType, nil, d.Pos)
			if !ok {
				continue
			}
			root.Node = node
			if _, seen := r.rootPos[node]; !seen {
				r.rootPos[node] = d.Pos
			}
		}
		r.activeScope = nil
	}

	// Extractor dependencies must be singletons: the extractor is what
	// decides which instance a caller gets, so it runs before any instance
	// exists. They are still resolved *inside* the scope, deliberately —
	// resolving them outside it would make a scoped dependency simply
	// unresolvable ("no provider for RoomKey"), which describes the symptom
	// rather than the mistake. Resolved here and rejected by
	// checkScopeEdges, the diagnostic can name the offending type instead.
	for _, s := range r.scopes {
		r.activeScope = s
		for _, root := range s.Roots {
			if root.Node == nil {
				continue
			}
			for i, p := range root.Extractor.Params {
				dep, ok := r.resolveKey(p, root.Extractor.ParamTypes[i], nil, root.Extractor.Pos)
				if !ok {
					continue
				}
				root.ExtractorDeps = append(root.ExtractorDeps, dep)
			}
		}
		r.activeScope = nil
	}

	r.assignMembership()
}

// assignMembership decides, for every node a scope's roots reach, whether
// it belongs to the scope or is a singleton the scope merely borrows.
//
// A node is a member if it is a declared root, or if any of its
// dependencies is the scope's key or is itself a member. Everything else —
// a logger, a config, a connection pool — is reached by the scope but does
// not vary with the key, so it stays a single app-level instance shared by
// every entry.
func (r *resolver) assignMembership() {
	for _, s := range r.scopes {
		reach := map[*Node]bool{}
		for _, root := range s.Roots {
			if root.Node != nil {
				r.collectReachable(root.Node, reach)
			}
			// Extractor dependencies are seeded too. One that takes the key
			// is scoped, and nothing else would notice: an extractor's
			// parameters are not the scoped type's constructor parameters,
			// so a walk from the roots alone never reaches them.
			for _, dep := range root.ExtractorDeps {
				r.collectReachable(dep, reach)
			}
		}

		member := map[*Node]bool{}
		for _, root := range s.Roots {
			if root.Node != nil {
				member[root.Node] = true
			}
		}
		for changed := true; changed; {
			changed = false
			for n := range reach {
				if member[n] {
					continue
				}
				for _, d := range n.Deps {
					if member[d] || (d.Kind == NodeScopeKey && d.Scope == s) {
						member[n] = true
						changed = true
						break
					}
				}
			}
		}

		// A node can only belong to one scope. When two scopes both claim
		// one — a type keyed by A whose constructor takes B's key, say —
		// nothing downstream would notice: the loser's members simply go
		// missing from its entry. So the conflict is recorded here, at the
		// only point where both claims are visible.
		for n := range member {
			if n.Scope != nil && n.Scope != s {
				r.claimConflicts = append(r.claimConflicts, claimConflict{node: n, first: n.Scope, second: s})
				continue
			}
			n.Scope = s
		}
	}
}

func (r *resolver) collectReachable(n *Node, seen map[*Node]bool) {
	if seen[n] || n.Kind != NodeProvider {
		return
	}
	seen[n] = true
	for _, d := range n.Deps {
		r.collectReachable(d, seen)
	}
}

// finishScopes partitions the single post-order construction list into the
// app's singletons and each scope's members, then levels each scope
// independently. Both partitions stay topologically ordered because the
// list they came from was: a scoped node's dependencies are either scoped
// (and earlier in the same partition) or singletons (already built by the
// App before any instance exists).
func (r *resolver) finishScopes() []*Node {
	var singletons []*Node
	for _, n := range r.order {
		if n.Scope == nil {
			singletons = append(singletons, n)
			continue
		}
		n.Scope.Order = append(n.Scope.Order, n)
	}
	for _, s := range r.scopes {
		for _, n := range s.Order {
			lvl := 1
			for _, d := range n.Deps {
				if d.Scope == s && d.Kind == NodeProvider && d.ScopeLevel >= lvl {
					lvl = d.ScopeLevel + 1
				}
			}
			n.ScopeLevel = lvl
		}
	}
	return singletons
}

// checkScopeEdges is the widening and cross-scope check, run over the
// finished graph rather than during traversal so it sees every edge
// exactly once no matter which pass discovered it — including the ones a
// second scope's sub-graph reaches into a first scope's.
//
// Widening is the reason this feature exists. A singleton holding a scoped
// instance pins one key's instance for the life of the process: the first
// room anyone joins becomes everyone's room, and nothing about the running
// program says so.
func (r *resolver) checkScopeEdges(roots []*Node) {
	// Sorted: claimConflicts is filled from a map range, and every other
	// diagnostic this package emits is driven by r.order and deterministic.
	sort.Slice(r.claimConflicts, func(i, j int) bool {
		return r.claimConflicts[i].node.Key.String() < r.claimConflicts[j].node.Key.String()
	})
	claimed := map[*Node]bool{}
	for _, c := range r.claimConflicts {
		claimed[c.node] = true
		r.diags = append(r.diags, r.claimedTwiceDiagnostic(c))
	}

	seen := map[string]bool{}
	for _, n := range r.order {
		for _, d := range n.Deps {
			// A dependency on a scope's *key* is a scope edge too, and the
			// one edge a membership fixpoint cannot classify by itself: a
			// node reached only through some other scope's sub-graph is a
			// member of neither, so nothing else would ever look at it.
			// Left unchecked it emits as a singleton with the key argument
			// silently dropped.
			if d.Kind == NodeScopeKey {
				// Already reported, and by the more specific message: a
				// node two scopes claim reaches this check only because
				// the first claimant's scope was left on it.
				if claimed[n] {
					continue
				}
				if n.Scope != d.Scope && !seen["k:"+n.Key.String()+">"+d.Key.String()] {
					seen["k:"+n.Key.String()+">"+d.Key.String()] = true
					r.diags = append(r.diags, r.strayKeyDiagnostic(n, d, roots))
				}
				continue
			}
			switch {
			case d.Scope == nil || d.Kind != NodeProvider:
				continue
			case n.Scope == nil:
				if !seen["w:"+n.Key.String()+">"+d.Key.String()] {
					seen["w:"+n.Key.String()+">"+d.Key.String()] = true
					r.diags = append(r.diags, r.wideningDiagnostic(n, d, roots))
				}
			case n.Scope != d.Scope:
				if !seen["x:"+n.Key.String()+">"+d.Key.String()] {
					seen["x:"+n.Key.String()+">"+d.Key.String()] = true
					r.diags = append(r.diags, r.crossScopeDiagnostic(n, d, roots))
				}
			}
		}
	}

	for _, s := range r.scopes {
		for _, root := range s.Roots {
			for _, dep := range root.ExtractorDeps {
				// An accessor is not an instance. It is a field on the App,
				// set before any constructor runs, so an extractor may hold
				// one — and doing so is the escape hatch every other scope
				// diagnostic points at.
				if dep.Kind == NodeScopeAccessor {
					// ...but not its own. Acquiring from inside the very
					// method that decides which instance to acquire is
					// unbounded recursion, and the one accessor edge that
					// cannot be a way out of anything.
					if dep.Scope == s {
						r.diags = append(r.diags, r.selfAcquiringExtractorDiagnostic(root, dep))
					}
					continue
				}
				if dep.Scope != nil {
					r.diags = append(r.diags, r.extractorCycleDiagnostic(root, dep, roots))
				}
			}
		}
	}

	// A root is the most singleton thing there is: the App holds it for the
	// life of the process. Declaring a scoped type as one is the widening
	// bug with the App itself as the consumer, and without this check it is
	// simply dropped — the node never reaches Resolved.Order, so no field
	// is emitted for it and nothing says why.
	for _, n := range roots {
		if n.Kind == NodeScopeAccessor {
			r.diags = append(r.diags, r.accessorRootDiagnostic(n))
			continue
		}
		if n.Scope != nil {
			r.diags = append(r.diags, r.scopedRootDiagnostic(n))
		}
	}
}

// checkAccessorInterfaces verifies each user-declared accessor interface
// against the accessor servo will actually emit. Without this the mismatch
// surfaces as a type error inside generated code — a file the user is told
// not to read, let alone edit.
func (r *resolver) checkAccessorInterfaces() {
	for _, s := range r.scopes {
		for _, root := range s.Roots {
			if root.Node == nil {
				continue
			}
			iface, ok := types.Unalias(root.IfaceType).Underlying().(*types.Interface)
			if !ok {
				continue // already reported by load
			}
			for i := 0; i < iface.NumMethods(); i++ {
				if err := accessorMethodOK(iface.Method(i), root.Node.Provider.ResultType); err != nil {
					r.diags = append(r.diags, Diagnostic{Pos: root.Pos, Message: fmt.Sprintf(
						"servo: scope accessor interface %s cannot be satisfied\n  %v\n\n  The generated accessor for %s has exactly two methods:\n      Acquire(ctx context.Context) (%s, func(), error)\n      Stats() servo.ScopeStats\n  Declare either or both, and nothing else.\n",
						root.Iface.String(), err, root.Node.Key.String(), root.Node.Key.String())})
				}
			}
		}
	}
}

// accessorMethodOK checks one method of a user-declared accessor interface
// against the shapes the generated accessor provides.
func accessorMethodOK(m *types.Func, impl types.Type) error {
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return fmt.Errorf("%s is not a method", m.Name())
	}
	switch m.Name() {
	case "Acquire":
		if sig.Params().Len() != 1 || !graph.IsContextType(sig.Params().At(0).Type()) {
			return fmt.Errorf("the Acquire method must take exactly one parameter, a context.Context")
		}
		if sig.Results().Len() != 3 {
			return fmt.Errorf("the Acquire method must return exactly three results, (%s, func(), error)", graph.TypeString(impl))
		}
		if !types.Identical(sig.Results().At(0).Type(), impl) {
			return fmt.Errorf("the Acquire method's first result is %s, but this scope hands out %s", graph.TypeString(sig.Results().At(0).Type()), graph.TypeString(impl))
		}
		if fn, ok := sig.Results().At(1).Type().Underlying().(*types.Signature); !ok || fn.Params().Len() != 0 || fn.Results().Len() != 0 {
			return fmt.Errorf("the Acquire method's second result must be func(), the release closure")
		}
		if !types.Identical(sig.Results().At(2).Type(), errorType) {
			return fmt.Errorf("the Acquire method's third result must be error")
		}
		return nil
	case "Stats":
		if sig.Params().Len() != 0 {
			return fmt.Errorf("the Stats method takes no parameters")
		}
		if sig.Results().Len() != 1 || !isScopeStatsType(sig.Results().At(0).Type()) {
			return fmt.Errorf("the Stats method must return exactly one result, servo.ScopeStats")
		}
		return nil
	default:
		return fmt.Errorf("it declares %s, which the generated accessor does not have", m.Name())
	}
}

func isScopeStatsType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == graph.ServoPackagePath && obj.Name() == "ScopeStats"
}

// ---- diagnostics ----------------------------------------------------

// chainTo reconstructs the shortest root → node path so a check run after
// traversal can still print the same "needed by" ladder an in-traversal
// diagnostic gets. Multi-source BFS over an acyclic graph, so the first
// arrival is a shortest path.
func (r *resolver) chainTo(roots []*Node, target *Node) ([]chainEntry, token.Position) {
	type queued struct {
		node *Node
		path []*Node
	}
	visited := map[*Node]bool{}
	var queue []queued
	push := func(n *Node) {
		if n == nil || visited[n] {
			return
		}
		visited[n] = true
		queue = append(queue, queued{n, []*Node{n}})
	}
	for _, n := range roots {
		push(n)
	}
	for _, s := range r.scopes {
		for _, root := range s.Roots {
			push(root.Node)
		}
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if item.node == target {
			entries := make([]chainEntry, 0, len(item.path))
			for _, n := range item.path {
				entries = append(entries, chainEntry{Key: n.Key, Label: n.Key.String(), Pos: nodePos(n)})
			}
			return entries, r.rootPos[item.path[0]]
		}
		for _, dep := range item.node.Deps {
			if visited[dep] || dep.Kind != NodeProvider {
				continue
			}
			visited[dep] = true
			queue = append(queue, queued{dep, append(append([]*Node{}, item.path...), dep)})
		}
	}
	return []chainEntry{{Key: target.Key, Label: target.Key.String(), Pos: nodePos(target)}}, r.rootPos[target]
}

func nodePos(n *Node) token.Position {
	if n.Provider != nil {
		return n.Provider.Pos
	}
	if n.Scope != nil {
		return n.Scope.Pos
	}
	return token.Position{}
}

// renderNeededBy prints the chain as "needed by" lines, deepest first,
// then the same trailing "root" line unresolvedDiagnostic renders — so a
// check that runs after traversal is indistinguishable, to a reader, from
// one that ran during it.
func renderNeededBy(chain []chainEntry, rootPos token.Position) string {
	if len(chain) == 0 {
		return ""
	}
	labels := make([]string, len(chain))
	maxLen := len("root")
	for i, f := range chain {
		labels[i] = "needed by " + f.Label
		if len(labels[i]) > maxLen {
			maxLen = len(labels[i])
		}
	}
	var b strings.Builder
	for i := len(chain) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "  %-*s  %s\n", maxLen, labels[i], chain[i].Pos)
	}
	// A node reached only as an extractor dependency descends from no
	// Root[] call, so there is no site to name. Printing the zero
	// token.Position renders a bare "-", which reads as a bug.
	if rootPos.IsValid() {
		fmt.Fprintf(&b, "  %-*s  %s\n", maxLen, "root", rootPos)
	}
	return b.String()
}

func (r *resolver) wideningDiagnostic(consumer, scoped *Node, roots []*Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is scoped, but %s is a singleton that depends on it\n",
		scoped.Key.String(), consumer.Key.String())
	b.WriteString(renderNeededBy(r.chainTo(roots, consumer)))
	fmt.Fprintf(&b, "\n  A singleton is constructed once and held for the life of the process, so it\n")
	fmt.Fprintf(&b, "  would capture whichever %s happened to be built first and hand that same\n", scoped.Key.String())
	fmt.Fprintf(&b, "  one to every caller afterwards, whatever key they present. Nothing about the\n")
	fmt.Fprintf(&b, "  running program would say so.\n\n")
	fmt.Fprintf(&b, "  Two ways out:\n")
	if iface := r.accessorIfaceFor(scoped); iface != "" {
		// The parameter's *declared* type, which may be an interface that
		// structural search or a Bind resolved to the scoped type. Naming
		// the resolved type would tell the user to change a parameter they
		// did not write.
		fmt.Fprintf(&b, "    - depend on the accessor instead: change %s's parameter from %s to %s,\n      and call Acquire(ctx) per request\n", consumer.Provider.Name, declaredParamOf(consumer, scoped), iface)
	} else if owners, accessors := r.scopeEntrancesTo(scoped); len(owners) > 0 {
		// The captured node is in the scope only because something else
		// reaches it, so it has no accessor to point at. Saying "depend on
		// the scope's accessor" without naming one is advice the reader
		// cannot follow, and following it literally does not typecheck:
		// that accessor hands out the type at the scope's entrance, not
		// this one.
		reaches := "reaches"
		if len(owners) > 1 {
			reaches = "reach"
		}
		fmt.Fprintf(&b, "    - %s has no accessor of its own. It is in this\n", scoped.Key.String())
		fmt.Fprintf(&b, "      scope because %s %s it, and only the type at a\n", strings.Join(owners, " and "), reaches)
		fmt.Fprintf(&b, "      scope's entrance gets one. Depend on %s instead,\n", strings.Join(accessors, " or "))
		fmt.Fprintf(&b, "      Acquire(ctx) per request, and reach %s through\n", scoped.Key.String())
		b.WriteString("      what it hands back\n")
	} else {
		fmt.Fprintf(&b, "    - depend on the scope's accessor interface instead of on %s directly,\n      and call Acquire(ctx) per request\n", scoped.Key.String())
	}
	fmt.Fprintf(&b, "    - make %s scoped too, by giving it a dependency on %s\n", consumer.Key.String(), scoped.Scope.KeyKey.String())
	return Diagnostic{Pos: nodePos(consumer), Message: b.String()}
}

// accessorIfaceFor names the interface a consumer should depend on
// instead, when the scoped node it captured is one the spec actually
// exposes. A scoped node reached only transitively has no accessor of its
// own, and the message says so differently.
func (r *resolver) accessorIfaceFor(scoped *Node) string {
	if scoped.Scope == nil {
		return ""
	}
	for _, root := range scoped.Scope.Roots {
		if root.Node == scoped {
			return root.Iface.String()
		}
	}
	return ""
}

// scopeEntrancesTo names the scoped types the spec exposes an accessor
// for *and* from which the captured node is actually reachable.
//
// Filtering matters: two servo.Scoped declarations sharing a key type are
// one scope with two roots, and only some of them may reach the node in
// hand. Listing all of them would tell the reader to acquire through an
// accessor that does not lead to what they are holding, which is the same
// does-not-typecheck advice this branch exists to replace.
func (r *resolver) scopeEntrancesTo(scoped *Node) (owners, accessors []string) {
	if scoped.Scope == nil {
		return nil, nil
	}
	for _, root := range scoped.Scope.Roots {
		if root.Node == nil {
			continue
		}
		reach := map[*Node]bool{}
		r.collectReachable(root.Node, reach)
		if !reach[scoped] {
			continue
		}
		owners = append(owners, root.Node.Key.String())
		accessors = append(accessors, root.Iface.String())
	}
	return owners, accessors
}

func (r *resolver) crossScopeDiagnostic(consumer, dep *Node, roots []*Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s and %s are in different scopes\n", consumer.Key.String(), dep.Key.String())
	b.WriteString(renderNeededBy(r.chainTo(roots, consumer)))
	fmt.Fprintf(&b, "\n  %s is keyed by %s\n", consumer.Key.String(), consumer.Scope.KeyKey.String())
	fmt.Fprintf(&b, "  %s is keyed by %s\n\n", dep.Key.String(), dep.Scope.KeyKey.String())
	b.WriteString("  Nested scopes are deliberately not supported in this release: one instance\n")
	b.WriteString("  per key pair means two reference counts and two linger windows with no single\n")
	b.WriteString("  owner, and no obvious answer for what happens when the outer one evicts while\n")
	b.WriteString("  the inner one is still held. This is a rejection, not an oversight.\n\n")
	fmt.Fprintf(&b, "  Depend on %s's accessor interface instead and Acquire it inside the method\n  that needs it, so the inner instance is held only for that call.\n", dep.Key.String())
	return Diagnostic{Pos: nodePos(consumer), Message: b.String()}
}

func (r *resolver) extractorCycleDiagnostic(root *ScopeRoot, dep *Node, roots []*Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s's %s extractor depends on %s, which is itself scoped\n",
		root.Extractor.Owner.String(), graph.ScopeKeyMethodName, dep.Key.String())
	fmt.Fprintf(&b, "  %s  %s\n", graph.ScopeKeyMethodName, root.Extractor.Pos)
	// The chain to the scoped dependency, not to the extractor: what the
	// reader has to change is somewhere along the path that made that
	// dependency scoped, and the extractor's own position is the line
	// above.
	b.WriteString(renderNeededBy(r.chainTo(roots, dep)))
	b.WriteString("\n  The extractor is what decides which instance a caller gets, so it runs before\n")
	b.WriteString("  any instance exists. Everything it takes must already be constructed — that is,\n")
	b.WriteString("  a singleton.\n")
	return Diagnostic{Pos: root.Extractor.Pos, Message: b.String()}
}

func (r *resolver) missingScopeKeyDiagnostic(d load.ScopeDecl) Diagnostic {
	return Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
		"servo: servo.Scoped[%s, %s] declares a scope, but %s has no %s method\n\n  Add one. The receiver must be unnamed — generated code calls it on a typed nil,\n  because it needs the key before it can choose an instance:\n\n\tfunc (%s) %s(ctx context.Context) (RoomKey, error) {\n\t    k, ok := ctx.Value(roomCtxKey{}).(RoomKey)\n\t    if !ok {\n\t        return \"\", servo.ErrNoScopeKey\n\t    }\n\t    return k, nil\n\t}\n\n  RoomKey stands for any defined type of your own; scope identity is that\n  type's identity.\n%s",
		d.Impl.String(), d.Iface.String(), d.Impl.String(), graph.ScopeKeyMethodName,
		localTypeString(d.ImplType), graph.ScopeKeyMethodName, promotedScopeKeyNote(d.ImplType))}
}

// undeclaredScopeDiagnostic is the mirror image: the method is there, the
// declaration is not.
func (r *resolver) undeclaredScopeDiagnostic(node *Node, methodPos token.Position, chain []chainEntry, rootPos token.Position) Diagnostic {
	local := localTypeString(node.Provider.ResultType)
	iface := suggestedIfaceName(node)
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s declares a %s method but no servo.Scoped declares it\n", node.Key.String(), graph.ScopeKeyMethodName)
	fmt.Fprintf(&b, "  %s  %s\n", graph.ScopeKeyMethodName, methodPos)
	// This check fires mid-traversal, before the node's own dependencies
	// are attached, so the chain comes from the DFS path that reached it
	// rather than from a walk of the finished graph — the same source the
	// unresolved and cycle diagnostics use at the same point.
	b.WriteString(renderNeededBy(chain, rootPos))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  A %s method is what makes a type keyed rather than a singleton, and servo\n", graph.ScopeKeyMethodName)
	b.WriteString("  will not infer the rest of the declaration from it: the accessor interface has\n")
	b.WriteString("  to be one you name, because servo cannot emit a type into your package.\n\n")
	if existing := existingAccessorIface(node); existing != "" {
		// Everything but the declaration is already there, so the message
		// is one line long and names the half that is missing.
		fmt.Fprintf(&b, "  %s.%s already has the shape the generated accessor satisfies, so all that is\n  missing is the declaration. In servo.Build:\n\n\tservo.Scoped[%s, %s](),\n\n",
			packageNameOf(node), existing, node.Key.String(), qualifiedIfaceName(node, existing))
	} else {
		fmt.Fprintf(&b, "  In package %s:\n\n\ttype %s interface {\n\t    Acquire(ctx context.Context) (%s, func(), error)\n\t}\n\n", packageNameOf(node), iface, local)
		fmt.Fprintf(&b, "  In servo.Build:\n\n\tservo.Scoped[%s, %s](),\n\n", node.Key.String(), qualifiedIfaceName(node, iface))
	}
	fmt.Fprintf(&b, "  Or delete the %s method, if this type is meant to be an ordinary singleton.\n", graph.ScopeKeyMethodName)
	return Diagnostic{Pos: methodPos, Message: b.String()}
}

// localTypeString renders a type the way it is written inside its own
// package — "*Room", not "*example.com/chat.Room" — for a snippet the
// reader is meant to paste into that package.
func localTypeString(t types.Type) string {
	if ptr, isPtr := types.Unalias(t).(*types.Pointer); isPtr {
		return "*" + localTypeString(ptr.Elem())
	}
	named := typeNameOf(t)
	if named == nil {
		return graph.TypeString(t)
	}
	name := named.Obj().Name()
	// An instantiated generic keeps its arguments: a suggested
	// `Acquire(ctx) (*Cache, ...)` for a *Cache[string] does not compile,
	// which makes the snippet worse than no snippet.
	if targs := named.TypeArgs(); targs != nil && targs.Len() > 0 {
		args := make([]string, targs.Len())
		for i := range args {
			args[i] = localTypeString(targs.At(i))
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name
}

func packageNameOf(n *Node) string {
	if named := typeNameOf(n.Provider.ResultType); named != nil && named.Obj().Pkg() != nil {
		return named.Obj().Pkg().Name()
	}
	return "yours"
}

// qualifiedIfaceName renders the suggested interface the way the servo
// markers name types — by full import path — since that is the form a
// spec file's type arguments take.
func qualifiedIfaceName(n *Node, short string) string {
	named := typeNameOf(n.Provider.ResultType)
	if named == nil || named.Obj().Pkg() == nil {
		return short
	}
	// short, not the scoped type's name plus an "s": when the package
	// already declares a satisfying accessor, short is that interface's
	// real name, and re-deriving one here would print a servo.Scoped line
	// naming a type that does not exist.
	return named.Obj().Pkg().Path() + "." + short
}

// existingAccessorIface names an interface already declared in the scoped
// type's own package that the generated accessor would satisfy, or "" if
// there is none.
//
// The likeliest way to arrive at the undeclared-scope diagnostic is to
// have written the type, the ScopeKey method and the accessor interface,
// and then forgotten only the servo.Scoped line. Telling that reader to
// declare an interface they are already looking at hands them a
// redeclaration error, and makes the message read as though servo had not
// understood their package.
func existingAccessorIface(n *Node) string {
	named := typeNameOf(n.Provider.ResultType)
	if named == nil || named.Obj().Pkg() == nil {
		return ""
	}
	scope := named.Obj().Pkg().Scope()
	for _, name := range scope.Names() {
		obj, isTypeName := scope.Lookup(name).(*types.TypeName)
		// Unexported would not be nameable from the spec file, which
		// lives in another package.
		if !isTypeName || !obj.Exported() {
			continue
		}
		iface, isIface := obj.Type().Underlying().(*types.Interface)
		if !isIface || iface.NumMethods() == 0 {
			continue
		}
		acquires, ok := false, true
		for i := 0; i < iface.NumMethods(); i++ {
			m := iface.Method(i)
			if accessorMethodOK(m, n.Provider.ResultType) != nil {
				ok = false
				break
			}
			if m.Name() == "Acquire" {
				acquires = true
			}
		}
		// Stats alone is satisfiable but useless as the thing a consumer
		// depends on, so it is not what this message should point at.
		if ok && acquires {
			return obj.Name()
		}
	}
	return ""
}

// suggestedIfaceName turns *chat.Room into Rooms, so the suggested
// declaration is something the reader can paste rather than a placeholder
// they have to translate.
func suggestedIfaceName(n *Node) string {
	named := typeNameOf(n.Provider.ResultType)
	if named == nil {
		return "Accessor"
	}
	return named.Obj().Name() + "s"
}

func typeNameOf(t types.Type) *types.Named {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return u
	case *types.Pointer:
		return typeNameOf(u.Elem())
	default:
		return nil
	}
}

func (r *resolver) namedReceiverDiagnostic(sk *graph.ScopeKey) Diagnostic {
	return Diagnostic{Pos: sk.Pos, Message: fmt.Sprintf(
		"servo: %s.%s must not name its receiver\n\n  Generated code calls it on a typed nil, because it needs the key before it can\n  choose an instance. A receiver the body can reach is a nil dereference waiting\n  to happen in production, and the type system has no way to rule it out — so an\n  unreachable receiver is required, not advisory:\n\n\tfunc (%s) %s(ctx context.Context) (%s, error)\n\n  A blank `_` receiver is accepted too, but omitting the name is preferred:\n  staticcheck's ST1006 flags `_` and asks for exactly this form.\n",
		sk.Owner.String(), graph.ScopeKeyMethodName, localTypeString(sk.OwnerType), graph.ScopeKeyMethodName, localTypeString(sk.KeyType))}
}

// scopeKeyOutsideScopeDiagnostic fires when a singleton asks for a key
// type directly. The key exists only inside the scope, produced per
// instance by the extractor, so there is nothing to hand it.
func (r *resolver) scopeKeyOutsideScopeDiagnostic(s *Scope, chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is a scope key and is not resolvable outside its scope\n", s.KeyKey.String())
	b.WriteString(renderChain(chain, rootPos))
	fmt.Fprintf(&b, "\n  A scope key is produced per instance by a %s method, at the moment someone\n", graph.ScopeKeyMethodName)
	b.WriteString("  acquires that instance. Outside the scope there is no instance and therefore no\n  key — and a provider returning this type, if one exists, is deliberately not\n  used for it: the whole point of the key is that it varies per acquire.\n\n")
	fmt.Fprintf(&b, "  If this consumer should be scoped, it becomes so by being reachable from a\n  scoped type; if it should not, take the value it needs as a method parameter\n  rather than a constructor parameter.\n")
	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
}

// claimConflict is one node two scopes both consider a member of theirs.
type claimConflict struct {
	node          *Node
	first, second *Scope
}

func (r *resolver) claimedTwiceDiagnostic(c claimConflict) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s belongs to two scopes at once\n", c.node.Key.String())
	fmt.Fprintf(&b, "  %s  and  %s\n", c.first.KeyKey.String(), c.second.KeyKey.String())
	fmt.Fprintf(&b, "  %s\n\n", nodePos(c.node))
	b.WriteString("  One instance per key *pair* is what a nested scope would mean: two reference\n")
	b.WriteString("  counts and two linger windows with no single owner. This release rejects that\n")
	b.WriteString("  rather than picking one of the two arbitrarily and quietly leaving the other\n")
	b.WriteString("  scope's entry without it.\n\n")
	fmt.Fprintf(&b, "  Depend on one of the two scopes' accessor interfaces instead of on its key, and\n  Acquire inside the method that needs it.\n")
	return Diagnostic{Pos: nodePos(c.node), Message: b.String()}
}

// strayKeyDiagnostic covers a dependency on a scope key held by something
// that is not in that scope: a singleton, or a member of a different one.
func (r *resolver) strayKeyDiagnostic(consumer, key *Node, roots []*Node) Diagnostic {
	var b strings.Builder
	if consumer.Scope == nil {
		fmt.Fprintf(&b, "servo: %s depends on %s, which is a scope key\n", consumer.Key.String(), key.Key.String())
	} else {
		fmt.Fprintf(&b, "servo: %s is keyed by %s but depends on %s, another scope's key\n",
			consumer.Key.String(), consumer.Scope.KeyKey.String(), key.Key.String())
	}
	b.WriteString(renderNeededBy(r.chainTo(roots, consumer)))
	fmt.Fprintf(&b, "\n  A scope key exists only inside its own scope, produced per instance by a\n  %s method at the moment someone acquires that instance. Taking one as a\n  constructor parameter is what puts a type *in* that scope — and %s is\n", graph.ScopeKeyMethodName, consumer.Key.String())
	if consumer.Scope == nil {
		b.WriteString("  not in it: nothing reachable from that scope's declared roots leads here.\n\n")
		fmt.Fprintf(&b, "  Either make it reachable from %s's sub-graph, or depend on that scope's\n  accessor interface and Acquire what you need per call.\n", key.Scope.KeyKey.String())
	} else {
		b.WriteString("  in a different one. Nested scopes are deliberately not supported.\n\n")
		fmt.Fprintf(&b, "  Depend on %s's accessor interface instead and Acquire inside the method that\n  needs it.\n", key.Scope.KeyKey.String())
	}
	return Diagnostic{Pos: nodePos(consumer), Message: b.String()}
}

func (r *resolver) scopedRootDiagnostic(n *Node) Diagnostic {
	iface := r.accessorIfaceFor(n)
	if iface == "" {
		iface = "that scope's accessor interface"
	}
	// The declared type, not the resolved one: the user may have written
	// servo.Root[SomeInterface] that structural search resolved to a
	// scoped type, and quoting what they did not write helps nobody.
	declared := r.specRootDecl[n]
	via := ""
	if declared != n.Key {
		via = fmt.Sprintf("  which resolves to %s\n", n.Key.String())
	}
	return Diagnostic{Pos: r.specRootPos[n], Message: fmt.Sprintf(
		"servo: servo.Root[%s] declares a scoped type as a root\n  %s\n%s\n  A root is held by the App for the life of the process, which is the one thing a\n  scoped instance must never be — it is the widening case with the App itself as\n  the consumer. A scope needs no root: its instances are reachable through\n  %s, and declaring the consumer that acquires them is enough.\n\n  Delete this servo.Root.\n",
		declared.String(), r.specRootPos[n], via, iface)}
}

func (r *resolver) accessorRootDiagnostic(n *Node) Diagnostic {
	return Diagnostic{Pos: r.specRootPos[n], Message: fmt.Sprintf(
		"servo: servo.Root[%s] declares a scope accessor as a root\n  %s\n\n  An accessor is generated code, not a node servo constructs, so there is nothing\n  here for a root to pull into the graph. Declare a root for the component that\n  takes %s as a dependency instead.\n",
		n.Key.String(), r.specRootPos[n], n.Key.String())}
}

// scopeKeyDiagnostic renders a graph.ScopeKeyError at the method's own
// position, rather than repeating it inside the message.
func scopeKeyDiagnostic(err error) Diagnostic {
	if ske, ok := errors.AsType[*graph.ScopeKeyError](err); ok {
		return Diagnostic{Pos: ske.Pos, Message: "servo: " + ske.Msg}
	}
	return Diagnostic{Message: "servo: " + err.Error()}
}

// boundAccessorDiagnostic covers servo.Bind or servo.Override naming a
// scope's accessor interface. Both are silently impossible: the accessor
// is emitted, not selected from candidates, so there is nothing for a
// binding to replace.
func (r *resolver) boundAccessorDiagnostic(root *ScopeRoot, concrete graph.Key) Diagnostic {
	at := r.explicitPos[root.Iface]
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is a scope accessor interface and cannot be bound or overridden\n", root.Iface.String())
	fmt.Fprintf(&b, "  bound to %s  %s\n", concrete.String(), at)
	fmt.Fprintf(&b, "  scope declared at %s\n\n", root.Pos)
	fmt.Fprintf(&b, "  servo emits the value that satisfies %s; it is not chosen from the\n", root.Iface.String())
	b.WriteString("  candidate index, so there is no selection for a Bind or an Override to change.\n")
	b.WriteString("  Accepting one silently would be worse than refusing it: a test App would go on\n")
	b.WriteString("  exercising the real scope while its spec file said otherwise.\n\n")
	fmt.Fprintf(&b, "  To test a consumer against a fake scope, give it %s as an ordinary\n", root.Iface.String())
	b.WriteString("  parameter and construct it directly in the test — an accessor is a two-method\n  interface, and a hand-written stand-in for it is a few lines.\n")
	pos := at
	if !pos.IsValid() {
		pos = root.Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
}

// accessorProviderDiagnostic is the same mistake made with a constructor
// instead of a marker. The accessor short-circuits ahead of provider
// selection, so such a function is an accepted candidate that resolution
// will never call — dead code with nothing in the spec file to hint at it.
func (r *resolver) accessorProviderDiagnostic(root *ScopeRoot, p *graph.Provider) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s produces %s, which is a scope accessor interface\n", p.Name, root.Iface.String())
	fmt.Fprintf(&b, "  %s\n", p.Pos)
	fmt.Fprintf(&b, "  scope declared at %s\n\n", root.Pos)
	fmt.Fprintf(&b, "  servo emits the value that satisfies %s, so this constructor would never\n", root.Iface.String())
	b.WriteString("  be called. Rather than leave it as dead code nothing in the spec file hints\n  at, servo refuses the pair.\n\n")
	b.WriteString("  Delete one of the two: the servo.Scoped declaration if this constructor is\n  what should provide the interface, or the constructor if the scope is.\n")
	return Diagnostic{Pos: p.Pos, Message: b.String()}
}

// promotedScopeKeyNote explains the one case where "no ScopeKey method" is
// technically true but reads as wrong: the method is reachable, just
// promoted from an embedded field.
func promotedScopeKeyNote(t types.Type) string {
	if !graph.PromotedScopeKey(t) {
		return ""
	}
	return fmt.Sprintf("\n  %s *is* reachable on this type, but through an embedded field. servo does not\n  use a promoted extractor: it calls the method on a typed nil, and the embedded\n  field of a typed nil is itself nil. Declare %s directly on %s.\n",
		graph.ScopeKeyMethodName, graph.ScopeKeyMethodName, graph.TypeString(t))
}

// declaredParamOf is the type consumer's constructor actually declares for
// the parameter that resolved to dep — which is not always dep's own type,
// since an interface parameter can reach a concrete node through a Bind or
// through structural search.
func declaredParamOf(consumer, dep *Node) string {
	for i, d := range consumer.Deps {
		if d == dep && i < len(consumer.Provider.ParamTypes) {
			return graph.TypeString(consumer.Provider.ParamTypes[i])
		}
	}
	return dep.Key.String()
}

// selfAcquiringExtractorDiagnostic covers a ScopeKey extractor that takes
// its own scope's accessor. Every other scope diagnostic points at an
// accessor as the way out; this is the one edge where that does not hold.
func (r *resolver) selfAcquiringExtractorDiagnostic(root *ScopeRoot, dep *Node) Diagnostic {
	return Diagnostic{Pos: root.Extractor.Pos, Message: fmt.Sprintf(
		"servo: %s's %s extractor takes %s, its own scope's accessor\n  %s\n\n  Acquire calls this method to decide which instance the caller gets, so an\n  Acquire from inside it would recurse without bound. Another scope's accessor is\n  fine here — that is the documented way out of a cross-scope dependency — but not\n  this one's.\n",
		root.Extractor.Owner.String(), graph.ScopeKeyMethodName, dep.Key.String(), root.Extractor.Pos)}
}
