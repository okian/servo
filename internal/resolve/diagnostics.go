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
