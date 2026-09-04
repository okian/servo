package resolve

import (
	"fmt"
	"go/token"
	"sort"
	"strings"

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

// unresolvedDiagnostic renders a missing provider and an ambiguous one in
// one shape, since both mean "no automatic resolution exists" — they
// differ only in whether there is a candidate list worth suggesting.
func (r *resolver) unresolvedDiagnostic(sel selection, chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: no provider for %s\n", sel.requested.String())
	b.WriteString(renderChain(chain, rootPos))

	if len(sel.candidates) > 0 {
		b.WriteString("\n")
		cands := sortedCandidates(sel.candidates)
		if sel.isInterface {
			fmt.Fprintf(&b, "  %d types implement %s — add one of:\n", len(cands), sel.requested.String())
			for _, c := range cands {
				fmt.Fprintf(&b, "      servo.Bind[%s, %s]()      %s\n", sel.requested.String(), c.Result.String(), c.Pos)
			}
		} else {
			fmt.Fprintf(&b, "  %d functions produce %s — remove or rename all but one:\n", len(cands), sel.requested.String())
			for _, c := range cands {
				fmt.Fprintf(&b, "      %s      %s\n", c.Name, c.Pos)
			}
		}
	}

	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
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

func sortedCandidates(cands []*graph.Provider) []*graph.Provider {
	sorted := append([]*graph.Provider(nil), cands...)
	sort.Slice(sorted, func(i, j int) bool { return graph.ComparePos(sorted[i].Pos, sorted[j].Pos) < 0 })
	return sorted
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

// configInScopeDiagnostic reports a scoped constructor depending on a
// //servo:config type. Config values are loaded at the top of New and live
// as locals there — never as App fields, which is what lets the type stay
// unexported — while a scope's per-key constructions read every borrowed
// singleton off the App. The two are incompatible, so it is refused with
// the workaround named rather than emitted as code that cannot compile.
func (r *resolver) configInScopeDiagnostic(d *graph.ConfigDecl, chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: a scoped constructor depends on config type %s\n", d.Key.String())
	b.WriteString(renderChain(chain, rootPos))
	b.WriteString("\n  A //servo:config value is loaded by New and held as a local, not an App\n")
	b.WriteString("  field, so a scope's per-key constructions cannot borrow it. Give the scoped\n")
	b.WriteString("  component a singleton of your own that carries the values it needs:\n")
	fmt.Fprintf(&b, "    func NewSettings(cfg %s) *Settings\n", d.TypeName)
	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
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
