package resolve

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// Diagnostic is one resolution failure. String renders it in
// "file:line:col: message" form, for editor integration, with Message's own
// multi-line body (the chain/candidate detail) following the leading
// position.
type Diagnostic struct {
	Pos     token.Position
	Message string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s: %s", d.Pos, d.Message)
}

func (d Diagnostic) Error() string { return d.String() }

// chainEntry is one frame of "how did we get here": the consumer's own
// result key, a display label, and its provider's declaration position.
type chainEntry struct {
	Key   graph.Key
	Label string
	Pos   token.Position
}

// chainLink is one frame of the needed-by path, linked to the frame that
// consumed it; nil is the empty path. A traversal pushes one link per node
// it enters and shares everything above it, so the path to a node 3,000
// levels down costs 3,000 links — where copying the path into a fresh slice
// per frame cost the square of that, about 790 MB for one such chain. It
// becomes a slice only when a diagnostic prints it.
type chainLink struct {
	entry  chainEntry
	parent *chainLink
}

func (l *chainLink) push(e chainEntry) *chainLink { return &chainLink{entry: e, parent: l} }

// linkOf links a root-first slice of frames.
func linkOf(entries []chainEntry) *chainLink {
	var l *chainLink
	for _, e := range entries {
		l = l.push(e)
	}
	return l
}

// entries is the path root-first, the order renderChain reads it in.
func (l *chainLink) entries() []chainEntry {
	n := 0
	for p := l; p != nil; p = p.parent {
		n++
	}
	out := make([]chainEntry, n)
	for p := l; p != nil; p = p.parent {
		n--
		out[n] = p.entry
	}
	return out
}

// unresolvedDiagnostic renders a missing provider and an ambiguous one.
// Both carry the needed-by chain; they differ in heading and body. An
// ambiguity is headed as one and lists what to write to settle it, in the
// form the spec file writes types. A missing provider lists the near
// misses: the functions that come close to providing the type, and why
// each does not — the variadic constructor, the unexported one, the one
// returning T where *T was asked for. Without them "no provider" sent the
// reader to `servo list --rejected` to find out that the function they
// wrote had been seen and refused.
func (r *resolver) unresolvedDiagnostic(sel selection, chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	switch {
	case len(sel.candidates) > 0:
		fmt.Fprintf(&b, "servo: ambiguous provider for %s\n", sel.requested.String())
	case sel.unusable > 0:
		fmt.Fprintf(&b, "servo: no usable provider for %s\n", sel.requested.String())
	default:
		fmt.Fprintf(&b, "servo: no provider for %s\n", sel.requested.String())
	}
	b.WriteString(renderChain(chain, rootPos))

	if len(sel.candidates) > 0 {
		b.WriteString("\n")
		r.writeAmbiguity(&b, sel)
	} else if lines := r.nearMissLines(sel); lines != "" {
		b.WriteString("\n")
		b.WriteString(lines)
	}

	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
}

// writeAmbiguity is the body of an ambiguity: the candidates, and what to
// do about them, which depends on whose they are.
func (r *resolver) writeAmbiguity(b *strings.Builder, sel selection) {
	cands := r.sortedCandidates(sel.candidates)
	spell := r.newSpecSpelling()
	want := sel.requested.String()
	if sel.requestedType != nil {
		want = spell.typeString(sel.requestedType)
	}
	// One line per candidate, positions in a column.
	listed := func(left func(*graph.Provider) string) {
		lefts := make([]string, len(cands))
		width := 0
		for i, c := range cands {
			lefts[i] = left(c)
			width = max(width, len(lefts[i]))
		}
		for i, c := range cands {
			fmt.Fprintf(b, "      %-*s  %s\n", width, lefts[i], c.Pos)
		}
	}
	byName := func(c *graph.Provider) string { return c.Name }
	switch {
	case sel.foreign:
		// None of them is the reader's to remove or rename, so the advice
		// is the two things they can do instead — and since the main
		// module's constructors now win, the first one settles it.
		fmt.Fprintf(b, "  %d functions outside this module produce %s, and servo will not pick one of\n  them for you:\n", len(cands), want)
		listed(byName)
		fmt.Fprintf(b, "\n  Write its provider in this module — a constructor of your own is preferred\n  over all of these:\n\n\tfunc New%s(...) %s\n\n", typeBaseName(sel.requestedType), want)
		fmt.Fprintf(b, "  or declare servo.Value[%s]() and hand one to NewWith.\n", want)
		writeSpecImports(b, spell)
	case sel.isInterface:
		fmt.Fprintf(b, "  %d types in this module implement %s — pick one with servo.Bind:\n", len(cands), want)
		listed(func(c *graph.Provider) string {
			return fmt.Sprintf("servo.Bind[%s, %s]()", want, spell.typeString(c.ResultType))
		})
		writeSpecImports(b, spell)
	default:
		// Renaming a function changes nothing: identity in the graph is
		// the type, so the way to have two is to make them two types.
		fmt.Fprintf(b, "  %d functions produce %s, and servo tells values apart by type alone, so they\n  are one contested node, not %d instances:\n", len(cands), want, len(cands))
		listed(byName)
		fmt.Fprintf(b, "\n  Delete all but one. If you want %d instances, give each its own named type and\n  have each constructor return its own — renaming a function cannot change the\n  type it returns:\n\n\ttype Primary%s struct{ %s }\n\n", len(cands), typeBaseName(sel.requestedType), want)
		b.WriteString("  \"Multiple instances of the same type\" in the README walks through it.\n")
	}
}

// nearMissLines renders the near misses for a missing provider, one per
// line, or "" when there are none.
func (r *resolver) nearMissLines(sel selection) string {
	if sel.requestedType == nil {
		return ""
	}
	misses := r.nearMissFinder().nearMisses(sel.requestedType)
	if len(misses) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range misses {
		fmt.Fprintf(&b, "  %s\n", m)
	}
	return b.String()
}

// nearMissFinder is the resolver's near-miss engine, built on first use:
// a graph that resolves never needs one.
func (r *resolver) nearMissFinder() *finder {
	if r.finder == nil {
		r.finder = newFinder(r.all, r.scope, r.pkgs, r.injectorPath, r.reach, r.byResult)
	}
	return r.finder
}

// typeBaseName is the name of the named type behind t — DB for *sql.DB —
// for the identifiers a suggestion makes up, or "Value" when t has none.
func typeBaseName(t types.Type) string {
	if named := typeNameOf(t); named != nil {
		return named.Obj().Name()
	}
	return "Value"
}

// specSpelling writes types the way the spec file does: by the name it
// imports each package under, bare for the injector's own package, and by
// package name for a package it does not import yet — collected, so a
// suggestion can say which imports to add. A suggested marker spelled with
// full import paths, `servo.Bind[example.com/app/store.Store, ...]()`, is
// not Go anyone can paste.
type specSpelling struct {
	injector string
	names    map[string]string // import path -> the name the spec file uses ("" for a dot import)
	used     map[string]string // name -> import path, for names already taken
	missing  map[string]specImport
}

type specImport struct {
	name  string
	alias bool
}

func (r *resolver) newSpecSpelling() *specSpelling {
	s := &specSpelling{injector: r.injectorPath, names: map[string]string{}, used: map[string]string{}, missing: map[string]specImport{}}
	if r.spec == nil || r.spec.File == nil {
		return s
	}
	imported := map[string]string{}
	if r.spec.InjectorPkg != nil && r.spec.InjectorPkg.Types != nil {
		for _, p := range r.spec.InjectorPkg.Types.Imports() {
			imported[p.Path()] = p.Name()
		}
	}
	for _, imp := range r.spec.File.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := imported[path]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch name {
		case "", "_":
			continue
		case ".":
			s.names[path] = ""
		default:
			s.names[path] = name
			s.used[name] = path
		}
	}
	return s
}

func (s *specSpelling) qualifier(p *types.Package) string {
	if p.Path() == s.injector {
		return ""
	}
	if name, ok := s.names[p.Path()]; ok {
		return name
	}
	if imp, ok := s.missing[p.Path()]; ok {
		return imp.name
	}
	imp := specImport{name: p.Name()}
	if other, taken := s.used[imp.name]; taken && other != p.Path() {
		imp = specImport{name: importAlias(p.Path(), s.used), alias: true}
	}
	s.used[imp.name] = p.Path()
	s.missing[p.Path()] = imp
	return imp.name
}

func (s *specSpelling) typeString(t types.Type) string {
	return graph.TypeStringWith(t, s.qualifier)
}

// importAlias extends a package's name backward one path element at a
// time — memory, storememory, appstorememory — until it is free, the way
// the emitter aliases colliding imports.
func importAlias(path string, used map[string]string) string {
	segments := strings.Split(path, "/")
	for i := len(segments) - 2; i >= 0; i-- {
		var b strings.Builder
		for _, r := range strings.Join(segments[i:], "") {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				b.WriteRune(unicode.ToLower(r))
			}
		}
		if candidate := b.String(); candidate != "" && used[candidate] == "" {
			return candidate
		}
	}
	return "pkg" + strconv.Itoa(len(used))
}

// writeSpecImports lists the imports a suggestion needs that the spec file
// does not have yet, sorted.
func writeSpecImports(b *strings.Builder, s *specSpelling) {
	if len(s.missing) == 0 {
		return
	}
	lines := make([]string, 0, len(s.missing))
	for path, imp := range s.missing {
		if imp.alias {
			lines = append(lines, imp.name+" "+strconv.Quote(path))
		} else {
			lines = append(lines, strconv.Quote(path))
		}
	}
	sort.Strings(lines)
	b.WriteString("  with these imports in the spec file:\n")
	for _, l := range lines {
		fmt.Fprintf(b, "      %s\n", l)
	}
}

// cycleDiagnostic renders the full loop path: chain's last frame is the
// node re-entered while gray; the loop is the chain suffix starting at
// that node's first occurrence.
func (r *resolver) cycleDiagnostic(chain []chainEntry) Diagnostic {
	last := chain[len(chain)-1]
	firstIdx := 0
	for i, f := range chain {
		if f.Key == last.Key {
			firstIdx = i
			break
		}
	}
	loop := chain[firstIdx:] // already ends back at the same key it started with

	var b strings.Builder
	b.WriteString("servo: dependency cycle detected\n")
	for i, f := range loop {
		suffix := ""
		if i == len(loop)-1 {
			suffix = "  (cycle closes here, back to the first line)"
		}
		fmt.Fprintf(&b, "  %-30s %s%s\n", f.Label, f.Pos, suffix)
	}

	return Diagnostic{Pos: loop[0].Pos, Message: b.String()}
}

// renderChain prints one "needed by <consumer>" line per chain frame,
// deepest/immediate-consumer first, followed by exactly one trailing "root"
// line at rootPos — even when chain's outermost frame is itself the root
// (that node is simultaneously "needed by" its own dependency and "a
// root", and both lines are printed for it).
func renderChain(chain []chainEntry, rootPos token.Position) string {
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
	fmt.Fprintf(&b, "  %-*s  %s\n", maxLen, "root", rootPos)
	return b.String()
}

func (r *resolver) sortedCandidates(cands []*graph.Provider) []*graph.Provider {
	sorted := append([]*graph.Provider(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return r.srcOrder.Compare(sorted[i].Pos, sorted[j].Pos) < 0 })
	return sorted
}

// unreachableConfigDiagnostic reports a //servo:config type whose
// generated loader the injector package cannot call.
func (r *resolver) unreachableConfigDiagnostic(d *graph.ConfigDecl, why string, chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: config type %s cannot be loaded by the injector in %s\n", d.Key.String(), r.injectorPath)
	b.WriteString(renderChain(chain, rootPos))
	fmt.Fprintf(&b, "\n  New calls the generated loader of every config its graph uses, and %s.\n", why)
	b.WriteString("  Declare the config type in a package the injector can import.\n")
	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
}

// scopedContextDiagnostic refuses a scoped constructor's context.Context.
func (r *resolver) scopedContextDiagnostic(n *Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s is scoped, but its constructor %s takes a context.Context\n", n.Key.String(), n.Provider.Name)
	fmt.Fprintf(&b, "  scoped at %s\n", n.Scope.Pos)
	b.WriteString("\n  A constructor's context.Context is the ctx New was called with, and a scoped\n")
	b.WriteString("  instance is built later, inside Acquire, after New has returned — there is no\n")
	b.WriteString("  construction context left to hand it. Do the work that needs a context in an\n")
	b.WriteString("  Init(ctx context.Context) error method instead, which servo calls as each\n  instance starts.\n")
	return Diagnostic{Pos: n.Provider.Pos, Message: b.String()}
}

// extractorContextDiagnostic refuses a context.Context among a ScopeKey
// extractor's dependencies.
func (r *resolver) extractorContextDiagnostic(root *ScopeRoot) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s's %s extractor takes a second context.Context\n", root.Extractor.Owner.String(), graph.ScopeKeyMethodName)
	b.WriteString("\n  Its first parameter is the context the instance is acquired with. Every\n")
	b.WriteString("  parameter after that is a singleton dependency, resolved once, so a\n")
	b.WriteString("  context.Context there would be the ctx New was called with, not this\n")
	b.WriteString("  acquire's. Read what you need from the first one.\n")
	return Diagnostic{Pos: root.Extractor.Pos, Message: b.String()}
}

// configProviderDiagnostic reports a hand-written constructor for a type
// that carries a //servo:config directive. The directive resolves ahead of
// provider selection, so the constructor would never be chosen — the same
// silent-loser problem a provider for a declared scope accessor has, and
// reported for the same reason.
func (r *resolver) configProviderDiagnostic(d *graph.ConfigDecl, c *graph.Provider) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: %s constructs %s, which carries a //servo:%s directive\n", c.Name, d.Key.String(), graph.ConfigDirective)
	b.WriteString("\n  The directive generates a loader for this type (ServoConfig, in its own\n")
	b.WriteString("  package), and resolution always prefers it — this constructor would sit in\n")
	b.WriteString("  the code looking authoritative and never run. Remove one:\n")
	fmt.Fprintf(&b, "    - delete the constructor      %s\n", c.Pos)
	fmt.Fprintf(&b, "    - or drop the directive       %s\n", d.Pos)
	return Diagnostic{Pos: c.Pos, Message: b.String()}
}

// configInScopeDiagnostic reports a scoped member's constructor taking a
// //servo:config type directly. Config values are loaded at the top of New
// and live as locals there — never as App fields, which is what lets the
// type stay unexported — while a scope's per-key constructions read every
// borrowed singleton off the App. The two are incompatible, so it is
// refused with the workaround named rather than emitted as code that
// cannot compile. A singleton between the member and the config is that
// workaround, and is legal.
func (r *resolver) configInScopeDiagnostic(d *graph.ConfigDecl, member *Node, scopePos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: a scoped constructor depends on config type %s\n", d.Key.String())
	fmt.Fprintf(&b, "  needed by %s  %s\n", member.Key.String(), member.Provider.Pos)
	fmt.Fprintf(&b, "  scoped at %s\n", scopePos)
	b.WriteString("\n  A //servo:config value is loaded by New and held as a local, not an App\n")
	b.WriteString("  field, so a scope's per-key constructions cannot take it directly. Give the\n")
	b.WriteString("  scoped component a singleton of your own that carries the values it needs —\n")
	b.WriteString("  a singleton is built by New, where the loaded value is in scope:\n")
	fmt.Fprintf(&b, "    func NewSettings(cfg %s) *Settings\n", d.TypeName)
	return Diagnostic{Pos: member.Provider.Pos, Message: b.String()}
}

// configCollisionDiagnostic reports two used configs resolving a setting
// to the same fully-qualified name. Nothing would fail at runtime — both
// fields would quietly read the same value — which is exactly why it has
// to fail here instead.
func (r *resolver) configCollisionDiagnostic(what, name string, firstDecl *graph.ConfigDecl, first graph.ConfigField, secondDecl *graph.ConfigDecl, second graph.ConfigField) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: two config fields resolve to the same %s %s\n", what, name)
	fmt.Fprintf(&b, "    %s.%s      %s\n", firstDecl.TypeName, first.FieldName, first.Pos)
	fmt.Fprintf(&b, "    %s.%s      %s\n", secondDecl.TypeName, second.FieldName, second.Pos)
	fmt.Fprintf(&b, "\n  Both would silently read one value. Change a tag name, or give one type a\n  different prefix in its //servo:%s directive.\n", graph.ConfigDirective)
	return Diagnostic{Pos: second.Pos, Message: b.String()}
}

// unusedConfigFileDiagnostic reports a servo.ConfigFile no config reads —
// the same judgement an unused servo.Value gets: an unresolvable
// declaration is a build failure, not a warning.
func (r *resolver) unusedConfigFileDiagnostic(decl *load.ConfigFileDecl) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: servo.ConfigFile(%q) is declared, but no //servo:%s type is in this graph\n", decl.Path, graph.ConfigDirective)
	b.WriteString("\n  The file would be read at startup and nothing would look inside it.\n")
	b.WriteString("  Either mark a config struct with the directive and take it as a constructor\n")
	b.WriteString("  parameter somewhere the roots reach, or delete the ConfigFile declaration.\n")
	return Diagnostic{Pos: decl.Pos, Message: b.String()}
}

// unusedValueDiagnostic reports a servo.Value nothing in the graph asks
// for. Every declared value becomes a field on the generated Values
// struct, so an unused one is a parameter every caller keeps supplying and
// the app never reads.
func (r *resolver) unusedValueDiagnostic(v load.ValueDecl) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: servo.Value[%s]() is declared, but nothing in the graph depends on %s\n", v.Key.String(), v.Key.String())
	b.WriteString("\n  A declared value becomes a field on the generated Values struct, so this\n")
	b.WriteString("  one would be supplied by every caller and read by nobody.\n\n")
	b.WriteString("  Two ways out:\n")
	fmt.Fprintf(&b, "    - take it as a constructor parameter somewhere the roots reach:\n      func New(v %s) *Thing\n", v.Key.String())
	b.WriteString("    - delete the servo.Value declaration\n")
	return Diagnostic{Pos: v.Pos, Message: b.String()}
}
