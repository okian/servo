package main

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

type explainJSON struct {
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	Pos          string   `json:"pos"`
	Binding      string   `json:"binding"`
	Level        int      `json:"level"`
	DependsOn    []string `json:"depends_on"`
	DependedOn   []string `json:"depended_on"`
	Capabilities []string `json:"capabilities"`
	// Scope is the key type this node is one instance per, empty for an
	// ordinary singleton; Lifetime spells the same thing out in words.
	Scope    string `json:"scope,omitempty"`
	Lifetime string `json:"lifetime"`
	// Binds is every servo.Bind in this graph that selected this node, set
	// when Binding is "explicit bind": the interface it binds and where it
	// is declared, which is not always the spec file.
	Binds []explainBindJSON `json:"binds,omitempty"`
	// Via is set when the query named an interface rather than a node: the
	// interface, and how it resolved to this node.
	Via *explainViaJSON `json:"via,omitempty"`
}

type explainBindJSON struct {
	Interface string `json:"interface"`
	Pos       string `json:"pos"`
	// Included marks a Bind spliced in from a marker set by servo.Include.
	Included bool `json:"included,omitempty"`
}

type explainViaJSON struct {
	Interface string `json:"interface"`
	// Binding is "explicit bind", "sole implementation" or "scope accessor".
	Binding  string `json:"binding"`
	Pos      string `json:"pos,omitempty"`
	Included bool   `json:"included,omitempty"`
}

// runExplain answers, for one node: which provider was selected and why,
// its position, direct dependencies, dependents, level, and capabilities.
func runExplain(cfg load.Config, query string, jsonOut bool) error {
	p, err := buildPipeline(cfg)
	if err != nil {
		return err
	}
	resolved, err := p.resolve(nil)
	if err != nil {
		return err
	}
	hops := interfaceHops(p.spec, resolved)
	node, via, err := lookupNode(p, resolved, hops, query)
	if err != nil {
		return err
	}

	deps := make([]string, len(node.Deps))
	for i, d := range node.Deps {
		deps[i] = d.Key.String()
	}
	// Each dependent is named once, however many of its parameters this
	// node answers: a constructor taking it as two interfaces depends on
	// it once, and listing that constructor twice read as two dependents.
	var dependents []string
	named := map[string]bool{}
	dependent := func(name string) {
		if !named[name] {
			named[name] = true
			dependents = append(dependents, name)
		}
	}
	for _, n := range allNodes(resolved) {
		for _, d := range n.Deps {
			if d.Key == node.Key {
				dependent(n.Key.String())
			}
		}
	}
	// A node the emitted HTTP servers pull in directly has no consumer
	// node either: the handler, middleware attachment, extractor or server
	// config that asked for it is named instead.
	for _, entry := range httpEntryPoints(resolved) {
		if entry.node == node {
			dependent("(" + entry.label + ")")
		}
	}
	// A scoped node's consumers are usually not other nodes at all: they
	// hold the accessor interface and acquire per call, so name that
	// instead of reporting "none" for something plainly in use.
	scopeKey, lifetime := "", "singleton — one per process, built by New"
	if node.Scoped() {
		scopeKey = node.Scope.KeyKey.String()
		lifetime = fmt.Sprintf("scoped — one per %s, linger %s, max %d", scopeKey, node.Scope.Linger, node.Scope.Max)
		for _, root := range node.Scope.Roots {
			if root.Node == node {
				dependent("(acquired via " + root.Iface.String() + ")")
			}
		}
	}

	// A servo.Value has no provider to name — the caller is the provider —
	// so the fields that would describe one carry the declaration instead.
	// A //servo:config's provider is its generated loader. A scope's key
	// comes from its ScopeKey extractor on every acquire, and the
	// construction context from whoever calls New.
	provider, pos, binding := "", "", node.Binding
	switch node.Kind {
	case resolve.NodeSupplied:
		provider, pos, binding = "the caller, via NewWith", node.SuppliedPos.String(), "supplied"
		lifetime = "supplied — handed to NewWith once, held for the life of the process"
	case resolve.NodeScopeKey:
		provider, pos = scopeKeyExtractors(node.Scope)
		binding = "scope key"
		lifetime = "per acquire — read from the acquiring context; the scope keeps one instance per key"
	case resolve.NodeContext:
		provider, pos = "the caller, as the ctx New, NewWith or NewTestApp is called with", p.spec.Pos.String()
		lifetime = "the call to New — for work done while constructing; Run's context is the one that lasts"
	case resolve.NodeConfig:
		provider = fmt.Sprintf("%s.ServoConfig (generated, env prefix %s)", node.Config.PkgName, node.Config.Prefix)
		pos = node.Config.Pos.String()
		lifetime = "config — loaded by New before construction, held for the life of the process"
	case resolve.NodeTraced:
		provider = fmt.Sprintf("generated OpenTelemetry wrapper around %s", node.Traced.Inner.Key.String())
		pos = node.Traced.Decl.Pos.String()
	default:
		provider, pos = node.Provider.Name, node.Provider.Pos.String()
	}

	// "explicit bind" names a decision somebody wrote down, so it says
	// where — the Bind may sit in a marker set pulled in with
	// servo.Include rather than in the spec file anyone would look in
	// first. A sole implementation names the interface it implements.
	var binds []explainBindJSON
	var implements []string
	for _, h := range hopsTo(hops, node) {
		switch h.binding {
		case bindingExplicit:
			binds = append(binds, explainBindJSON{Interface: h.iface.String(), Pos: h.pos.String(), Included: h.included})
		case bindingSole:
			implements = append(implements, h.iface.String())
		}
	}
	bindingText := binding
	switch {
	case binding == bindingExplicit && len(binds) > 0:
		where := make([]string, len(binds))
		for i, b := range binds {
			where[i] = b.Interface + ", " + b.Pos
			if b.Included {
				where[i] += ", via servo.Include"
			}
		}
		bindingText += " (" + strings.Join(where, "; ") + ")"
	case binding == bindingSole && len(implements) > 0:
		bindingText += " (of " + strings.Join(implements, ", ") + ")"
	}
	if binding != bindingExplicit {
		binds = nil
	}

	if jsonOut {
		out := explainJSON{
			Type: node.Key.String(), Provider: provider, Pos: pos,
			Binding: binding, Level: levelOf(node),
			DependsOn: deps, DependedOn: dependents, Capabilities: node.Capabilities,
			Scope: scopeKey, Lifetime: lifetime, Binds: binds,
		}
		if via != nil {
			out.Via = &explainViaJSON{Interface: via.iface.String(), Binding: via.binding, Included: via.included}
			if via.binding != bindingSole {
				out.Via.Pos = via.pos.String()
			}
		}
		return printJSON(out)
	}

	if via != nil {
		fmt.Printf("%s resolves to %s — %s\n\n", via.iface.String(), node.Key.String(), via.how())
	}
	fmt.Printf("%s\n", node.Key.String())
	fmt.Printf("  provider:     %s (%s)\n", provider, pos)
	fmt.Printf("  binding:      %s\n", bindingText)
	fmt.Printf("  lifetime:     %s\n", lifetime)
	fmt.Printf("  level:        %d\n", levelOf(node))
	fmt.Printf("  depends on:   %s\n", joinOrNone(deps))
	fmt.Printf("  depended on:  %s\n", joinOrNone(dependents))
	fmt.Printf("  capabilities: %s\n", joinOrNone(node.Capabilities))
	return nil
}

// scopeKeyExtractors names the ScopeKey methods a scope's key comes from,
// one per servo.Scoped sharing the key type, and where each is declared.
func scopeKeyExtractors(s *resolve.Scope) (names, positions string) {
	var ns, ps []string
	for _, root := range s.Roots {
		if root.Extractor == nil {
			continue
		}
		ns = append(ns, "("+shortTypeString(root.Extractor.OwnerType)+")."+graph.ScopeKeyMethodName)
		ps = append(ps, root.Extractor.Pos.String())
	}
	return strings.Join(ns, ", ") + ", on each acquire", strings.Join(ps, ", ")
}

// levelOf reports a scoped node's level within its own scope, and an
// ordinary node's within the app. Reporting a scoped node's app-level
// depth would say more about how deep the singletons it borrows sit than
// about when it is constructed.
func levelOf(n *resolve.Node) int {
	if n.Scoped() {
		return n.ScopeLevel
	}
	return n.Level
}

// The three ways a requested interface reaches a node of another type.
// The first two are the resolver's own Binding values.
const (
	bindingExplicit = "explicit bind"
	bindingSole     = "sole implementation"
	bindingAccessor = "scope accessor"
)

// hop is one interface the graph asked for and the node it was handed
// instead. Nodes are keyed by the type a provider returns, so a query
// naming the interface — the type people actually read in a constructor's
// parameter list — matched no node at all, and a path through one showed
// the concrete type with no sign of the interface that led there.
type hop struct {
	iface     graph.Key
	ifaceType types.Type
	node      *resolve.Node
	binding   string // bindingExplicit, bindingSole or bindingAccessor
	// pos is the servo.Bind, or the servo.Scoped an accessor is generated
	// for; a sole implementation has no declaration to point at.
	pos token.Position
	// included marks a Bind that came from a marker set pulled in with
	// servo.Include.
	included bool
}

// how says, in a phrase, how h's interface reached its node.
func (h *hop) how() string {
	switch h.binding {
	case bindingExplicit:
		where := "servo.Bind at " + h.pos.String()
		if h.included {
			where += ", in a marker set pulled in with servo.Include"
		}
		return bindingExplicit + ", " + where
	case bindingAccessor:
		return "the scope accessor servo generates for the servo.Scoped at " + h.pos.String() + "; each acquire hands out one"
	default:
		return "the sole implementation in the main module"
	}
}

// interfaceHops indexes every interface this graph requested — through a
// constructor parameter, a servo.Root, a handler argument, a servo.Use, a
// scope extractor's parameter, or a trace wrapper's implementation — by
// the interface's key. Every request for one key resolves to the same
// node, so the first one seen answers for all of them.
func interfaceHops(spec *load.Spec, resolved *resolve.Resolved) map[graph.Key]*hop {
	// The resolver reads the binds in order and the last one for an
	// interface wins, so the same holds here.
	binds := map[graph.Key]load.BindDecl{}
	for _, b := range spec.Binds {
		binds[b.Iface] = b
	}
	hops := map[graph.Key]*hop{}
	note := func(requested graph.Key, requestedType types.Type, got *resolve.Node) {
		if got == nil || hops[requested] != nil {
			return
		}
		switch {
		case got.Kind == resolve.NodeScopeAccessor:
			if got.ScopeRoot != nil && got.ScopeRoot.Node != nil {
				hops[requested] = &hop{iface: requested, ifaceType: requestedType, node: got.ScopeRoot.Node, binding: bindingAccessor, pos: got.ScopeRoot.Pos}
			}
		case got.Kind == resolve.NodeTraced, got.Key == requested:
			// No detour to explain: a trace wrapper is found by the
			// interface it implements, and an exact key is no hop at all.
		default:
			h := &hop{iface: requested, ifaceType: requestedType, node: got, binding: bindingSole}
			if b, ok := binds[requested]; ok {
				h.binding, h.pos, h.included = bindingExplicit, b.Pos, b.Included
			}
			hops[requested] = h
		}
	}

	// Each list below is parallel to the requests that produced it: a
	// graph that resolved at all resolved every one of them, in order.
	if len(spec.Roots) == len(resolved.Roots) {
		for i, rd := range spec.Roots {
			note(rd.Key, rd.Type, resolved.Roots[i])
		}
	}
	for _, n := range allNodes(resolved) {
		switch {
		case n.Provider != nil && len(n.Provider.Params) == len(n.Deps):
			for i, k := range n.Provider.Params {
				note(k, n.Provider.ParamTypes[i], n.Deps[i])
			}
		case n.Kind == resolve.NodeTraced && n.Traced != nil:
			note(graph.Key{Type: n.Key.Type}, n.Traced.IfaceType, n.Traced.Inner)
		}
	}
	for _, s := range resolved.Scopes {
		for _, root := range s.Roots {
			if root.Extractor != nil && len(root.Extractor.Params) == len(root.ExtractorDeps) {
				for i, k := range root.Extractor.Params {
					note(k, root.Extractor.ParamTypes[i], root.ExtractorDeps[i])
				}
			}
		}
	}
	if plan := resolved.HTTP; plan != nil {
		for _, hr := range plan.Routes {
			if len(hr.Route.Deps) != len(hr.Args) {
				continue
			}
			for i, k := range hr.Route.Deps {
				note(k, hr.Route.DepTypes[i], hr.Args[i].Node)
			}
		}
		for _, u := range plan.Uses {
			note(u.Decl.Type, u.Decl.TypeT, u.Node)
		}
	}
	return hops
}

// hopsTo is every hop resolving to n, in interface order.
func hopsTo(hops map[graph.Key]*hop, n *resolve.Node) []*hop {
	var out []*hop
	for _, h := range hops {
		if h.node == n {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].iface.String() < out[j].iface.String() })
	return out
}

// lookupNode finds the node an explain or why query names. findNode
// matches node keys exactly or by a suffix, which leaves out the
// spellings people actually type: Go's own pointer syntax, `*api.Server`,
// which no key ends with; a generic instantiation qualified the way the
// source writes it, `repo.Repo[model.User]`, where every key carries
// full import paths inside the brackets too; and an interface such as
// `store.Store`, which is a parameter type rather than a node and
// resolves to one. Those are tried only once findNode has found nothing,
// so what used to work keeps meaning what it meant, and findNode's own
// ambiguity error still comes back unchanged. via is the interface's
// resolution, nil when the query named a node.
func lookupNode(p *pipeline, resolved *resolve.Resolved, hops map[graph.Key]*hop, query string) (*resolve.Node, *hop, error) {
	node, err := findNode(resolved, query)
	if err == nil {
		return node, nil, nil
	}
	if keyMatchesAny(resolved, query) {
		return nil, nil, err
	}

	q := canonType(query)
	if q == "" {
		return nil, nil, err
	}
	if n, err := matchNodeForms(allNodes(resolved), q, query); n != nil || err != nil {
		return n, nil, err
	}
	if h, err := matchHops(hops, q, query); h != nil || err != nil {
		if err != nil {
			return nil, nil, err
		}
		return h.node, h, nil
	}
	return nil, nil, absentError(p, query)
}

// keyMatchesAny is findNode's own matching rule, asked only whether
// anything matched — so its ambiguity error can be told apart from its
// "no node matches" without reading the message.
func keyMatchesAny(resolved *resolve.Resolved, query string) bool {
	for _, n := range allNodes(resolved) {
		if n.Key.String() == query || n.Key.Type == query || matchesSuffix(n.Key.Type, query) {
			return true
		}
	}
	return false
}

// canonType is a type string with every space removed, so the go/types
// spelling `Pair[string, int]` and the typed one `Pair[string,int]`
// compare equal.
func canonType(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// shortTypeString renders t the way source code spells it, with package
// names where a key has import paths: `*repo.Repo[model.User]` for the
// key `*example.com/app/repo.Repo[example.com/app/model.User]`. Aliases
// are resolved as the key's own rendering resolves them.
func shortTypeString(t types.Type) string {
	return types.TypeString(unaliasPointers(t), func(p *types.Package) string { return p.Name() })
}

func unaliasPointers(t types.Type) types.Type {
	u := types.Unalias(t)
	if ptr, ok := u.(*types.Pointer); ok {
		return types.NewPointer(unaliasPointers(ptr.Elem()))
	}
	return u
}

// nodeType is the Go type a node stands for, nil for a kind that has
// none worth spelling.
func nodeType(n *resolve.Node) types.Type {
	switch {
	case n.Provider != nil:
		return n.Provider.ResultType
	case n.Kind == resolve.NodeSupplied:
		return n.SuppliedType
	case n.Kind == resolve.NodeConfig && n.Config != nil:
		return n.Config.Type
	case n.Kind == resolve.NodeTraced && n.Traced != nil:
		return n.Traced.IfaceType
	case n.Kind == resolve.NodeScopeKey && n.Scope != nil:
		return n.Scope.KeyType
	}
	return nil
}

// typeForms are the canonical spellings a type answers a query to: its
// key, and its package-name form when the type is known. A query with no
// type arguments also answers to an instantiation by its generic type's
// name — `repo.Repo` finds `*repo.Repo[model.User]`, or, with two
// instantiations in the graph, names both.
func typeForms(key string, t types.Type, q string) []string {
	forms := []string{canonType(key)}
	if t != nil {
		forms = append(forms, canonType(shortTypeString(t)))
	}
	if !strings.Contains(q, "[") {
		for _, f := range forms {
			if i := strings.IndexByte(f, '['); i > 0 {
				forms = append(forms, f[:i])
			}
		}
	}
	return forms
}

// formMatch reports how q names a type spelled form: exactly, by a
// suffix starting at a name boundary, or not at all. A query with a
// leading star asks for a pointer, so it matches only a pointer's form,
// and is compared without both stars: `*Server` is the elem `Server` of
// `*api.Server`, a suffix the starred strings themselves do not share.
func formMatch(form, q string) (exact, suffix bool) {
	if elem, ok := strings.CutPrefix(q, "*"); ok {
		formElem, isPtr := strings.CutPrefix(form, "*")
		if !isPtr {
			return false, false
		}
		form, q = formElem, elem
	}
	if form == q {
		return true, false
	}
	return false, matchesSuffix(form, q)
}

// matchNodeForms matches q against every node's spellings: a unique exact
// match first, then a unique suffix match; more than one at the deciding
// tier is an ambiguity, named.
func matchNodeForms(nodes []*resolve.Node, q, query string) (*resolve.Node, error) {
	var exact, suffix []*resolve.Node
	for _, n := range nodes {
		e, s := false, false
		for _, form := range typeForms(n.Key.String(), nodeType(n), q) {
			fe, fs := formMatch(form, q)
			e, s = e || fe, s || fs
		}
		switch {
		case e:
			exact = append(exact, n)
		case s:
			suffix = append(suffix, n)
		}
	}
	for _, tier := range [][]*resolve.Node{exact, suffix} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			names := make([]string, len(tier))
			for i, n := range tier {
				names[i] = n.Key.String()
			}
			return nil, fmt.Errorf("servo: %q matches multiple nodes, be more specific: %s", query, strings.Join(names, ", "))
		}
	}
	return nil, nil
}

// matchHops is matchNodeForms for the interfaces the graph requested.
func matchHops(hops map[graph.Key]*hop, q, query string) (*hop, error) {
	if strings.HasPrefix(q, "*") {
		return nil, nil // a pointer to an interface is never a request servo resolves
	}
	var exact, suffix []*hop
	for _, h := range hops {
		e, s := false, false
		for _, form := range typeForms(h.iface.String(), h.ifaceType, q) {
			fe, fs := formMatch(form, q)
			e, s = e || fe, s || fs
		}
		switch {
		case e:
			exact = append(exact, h)
		case s:
			suffix = append(suffix, h)
		}
	}
	for _, tier := range [][]*hop{exact, suffix} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			names := make([]string, len(tier))
			for i, h := range tier {
				names[i] = h.iface.String()
			}
			sort.Strings(names)
			return nil, fmt.Errorf("servo: %q matches multiple interfaces, be more specific: %s", query, strings.Join(names, ", "))
		}
	}
	return nil, nil
}

// absentError answers the question a failed lookup leaves open: not "no
// node matches", which says nothing about why, but what servo knows about
// the type the query names — the constructor that provides it but that no
// root reaches, the candidates implementing an interface nothing asks
// for, the Override that puts it only in NewTestApp's graph, the function
// that returns it but was rejected and the rule that rejected it.
//
// It reads the candidate index and the rejected list the pipeline already
// holds; the resolver's own near-miss reporting for "no provider for T"
// is a separate question, asked at resolution time.
func absentError(p *pipeline, query string) error {
	head := fmt.Sprintf("servo: no node matches %q", query)
	if p == nil || p.loaded == nil {
		return errors.New(head)
	}
	found := typesNamed(p, query)
	if len(found) == 0 {
		return fmt.Errorf("%s — no type of that name is in this graph, or in any package this run loaded (the whole main module and everything it imports); `servo list` shows every constructor servo considered", head)
	}
	var b strings.Builder
	b.WriteString(head)
	if len(found) > 1 {
		names := make([]string, len(found))
		for i, tn := range found {
			names[i] = declName(tn.Type(), false)
		}
		fmt.Fprintf(&b, " — and it names %d types, none of them in this graph: %s", len(found), strings.Join(names, ", "))
		return errors.New(b.String())
	}
	t := found[0].Type()
	key := declName(t, false)
	if types.IsInterface(t) {
		fmt.Fprintf(&b, " — %s is an interface, and nothing in this graph asks for it\n", key)
		describeAbsentInterface(&b, p, t)
	} else {
		fmt.Fprintf(&b, " — %s is not in this graph\n", key)
		describeAbsentConcrete(&b, p, t, strings.HasPrefix(strings.TrimSpace(query), "*"))
	}
	return errors.New(strings.TrimRight(b.String(), "\n"))
}

// describeAbsentInterface lists what would answer a request for iface if
// something made one: the Binds naming it, and the main-module candidates
// implementing it.
func describeAbsentInterface(b *strings.Builder, p *pipeline, iface types.Type) {
	key := graph.TypeString(iface)
	for _, bd := range p.spec.Binds {
		if bd.Iface.Type == key {
			fmt.Fprintf(b, "  servo.Bind at %s binds it to %s, but no constructor, root or handler requests it\n", bd.Pos, bd.Concrete)
		}
	}
	shown := 0
	for _, c := range p.candidates {
		if !p.scope[c.Pkg] || c.ResultType == nil || types.IsInterface(c.ResultType) {
			continue
		}
		if !types.Implements(c.ResultType, iface.Underlying().(*types.Interface)) {
			continue
		}
		if shown == 10 {
			b.WriteString("  … and more; `servo list` has every candidate\n")
			break
		}
		fmt.Fprintf(b, "  implemented by %s (%s, %s)\n", c.Result, c.Name, c.Pos)
		shown++
	}
	if shown == 0 {
		b.WriteString("  and no constructor in the main module returns a type implementing it\n")
	}
}

// describeAbsentConcrete says why a concrete type that exists is not in
// the graph: a provider no root reaches, an Override that puts it only in
// the test graph, a function returning it that the scan rejected — or no
// constructor at all.
func describeAbsentConcrete(b *strings.Builder, p *pipeline, t types.Type, askedPointer bool) {
	named := false
	for _, c := range p.candidates {
		if c.ResultType == nil || !sameOrPointer(c.ResultType, t) {
			continue
		}
		named = true
		// An unstarred query matches pointer nodes as readily as values,
		// so only a star asking for what the provider does not return is
		// worth a word.
		note := ""
		if _, isPtr := types.Unalias(c.ResultType).(*types.Pointer); askedPointer && !isPtr {
			note = fmt.Sprintf(" (note: it returns the value %s, not a pointer)", c.Result)
		}
		fmt.Fprintf(b, "  %s (%s) provides %s, but nothing in this graph depends on it, so no root reaches it — declare servo.Root[%s](), or take it as a constructor parameter%s\n",
			c.Name, c.Pos, c.Result, shortTypeString(c.ResultType), note)
		for _, o := range p.spec.Overrides {
			if o.Concrete == c.Result {
				fmt.Fprintf(b, "  servo.Override at %s puts it in NewTestApp's graph in place of %s; explain and why answer for New's\n", o.Pos, o.Iface)
			}
		}
	}
	for _, r := range p.rejected {
		if fn := rejectedFunc(p, r); fn != nil && returnsType(fn, t) {
			named = true
			fmt.Fprintf(b, "  %s (%s) returns it, but servo rejected it: %s\n", r.Name, r.Pos, r.Reason)
		}
	}
	if !named {
		fmt.Fprintf(b, "  and no function in the module returns %s or a pointer to it\n", declName(t, true))
	}
}

// sameOrPointer reports whether got is t or *t — or, for a generic t,
// any instantiation of it or a pointer to one, since a generic type is
// queried by its declaration.
func sameOrPointer(got, t types.Type) bool {
	got = types.Unalias(got)
	if ptr, ok := got.(*types.Pointer); ok {
		got = types.Unalias(ptr.Elem())
	}
	if types.Identical(got, t) {
		return true
	}
	gn, ok1 := got.(*types.Named)
	tn, ok2 := t.(*types.Named)
	return ok1 && ok2 && tn.TypeParams().Len() > 0 && gn.Origin() == tn.Origin()
}

// declName spells a declared type for a message: the package-qualified
// name, with a generic type's parameter list shortened to [...] — the
// declaration, not an instantiation anybody could request.
func declName(t types.Type, short bool) string {
	if n, ok := t.(*types.Named); ok && n.TypeParams().Len() > 0 {
		qual := n.Obj().Pkg().Path()
		if short {
			qual = n.Obj().Pkg().Name()
		}
		return qual + "." + n.Obj().Name() + "[...]"
	}
	if short {
		return shortTypeString(t)
	}
	return graph.TypeString(t)
}

// rejectedFunc is the package-level function a rejected entry names, nil
// for a method or anything the loaded packages cannot account for.
func rejectedFunc(p *pipeline, r graph.Rejected) *types.Func {
	pkg := p.loaded.ByPath[r.Pkg]
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	_, name, ok := strings.Cut(r.Name, ".")
	if !ok || strings.ContainsAny(name, ".()") {
		return nil
	}
	fn, _ := pkg.Types.Scope().Lookup(name).(*types.Func)
	return fn
}

// returnsType reports whether any of fn's results is t or *t.
func returnsType(fn *types.Func, t types.Type) bool {
	results := fn.Type().(*types.Signature).Results()
	for i := range results.Len() {
		if sameOrPointer(results.At(i).Type(), t) {
			return true
		}
	}
	return false
}

// typesNamed finds the declared types a query names — by import path or
// package name, any pointer star and type arguments dropped, since a
// generic type's instantiation is not a declaration — preferring the main
// module's over a dependency's of the same name.
func typesNamed(p *pipeline, query string) []*types.TypeName {
	q := canonType(strings.TrimPrefix(strings.TrimSpace(query), "*"))
	if i := strings.IndexByte(q, '['); i >= 0 {
		q = q[:i]
	}
	if q == "" {
		return nil
	}
	seen := map[types.Type]bool{}
	var mainModule, others []*types.TypeName
	for _, pkg := range p.loaded.All {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			full, short := pkg.PkgPath+"."+name, pkg.Types.Name()+"."+name
			if _, fs := formMatch(full, q); full != q && short != q && !fs {
				if _, ss := formMatch(short, q); !ss {
					continue
				}
			}
			// An alias stands for its target, which is reported once
			// whichever of its names the query used.
			target := types.Unalias(tn.Type())
			named, ok := target.(*types.Named)
			if !ok || seen[target] {
				continue
			}
			seen[target] = true
			if p.scope[pkg.PkgPath] {
				mainModule = append(mainModule, named.Obj())
			} else {
				others = append(others, named.Obj())
			}
		}
	}
	if len(mainModule) > 0 {
		return mainModule
	}
	return others
}
