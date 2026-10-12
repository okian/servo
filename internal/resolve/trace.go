package resolve

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// TraceInput is the resolver's slice of the OpenTelemetry trace API: the
// key and type of trace.TracerProvider, the node every generated wrapper
// depends on. Nil when the package is not in the module's build, which
// becomes a diagnostic the moment a spec declares servo.Trace — the
// wrapper cannot be written against a package the build cannot see.
type TraceInput struct {
	ProviderKey  graph.Key
	ProviderType types.Type
}

// Traced is set only on NodeTraced nodes: the declaration that asked for
// the wrapper, the interface it implements, and the two nodes it is built
// from. Deps holds the same two nodes, in this order, so every walk over
// Deps sees the real edges.
type Traced struct {
	Decl      *load.TraceDecl
	IfaceType types.Type
	Inner     *Node // whatever the interface resolved to
	Tracer    *Node // the trace.TracerProvider node
}

// TracedTag is the Key tag a wrapper node carries. The traced view of an
// interface is a different node from a provider that happens to return
// the interface itself, and the tag is what keeps the two keys apart.
const TracedTag = "traced"

// tracedKey is the wrapper node's key for a traced interface.
func tracedKey(iface graph.Key) graph.Key {
	return graph.Key{Type: iface.Type, Tag: TracedTag}
}

// setupTraces indexes the spec's Trace declarations and rejects the ones
// no traversal could make sense of: a declaration with no trace API to
// build against, and an interface that is a scope's accessor or key —
// generated code and extracted values respectively, with no provider
// behind them to wrap.
func (r *resolver) setupTraces(in Input) {
	if len(in.Spec.Traces) == 0 {
		return
	}
	if in.Trace == nil {
		for i := range in.Spec.Traces {
			r.diags = append(r.diags, r.missingOtelDiagnostic(&in.Spec.Traces[i]))
		}
		return
	}
	r.traceIn = in.Trace
	for i := range in.Spec.Traces {
		d := &in.Spec.Traces[i]
		switch {
		case r.accessorByKey[d.Iface] != nil:
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
				"servo: servo.Trace[%s]: %s is a scope accessor interface — generated code, not a provider — so there is nothing to wrap\n\n  Trace the interfaces the scoped type depends on instead.\n",
				d.Iface.String(), d.Iface.String())})
		case r.scopeByKey[d.Iface] != nil:
			r.diags = append(r.diags, Diagnostic{Pos: d.Pos, Message: fmt.Sprintf(
				"servo: servo.Trace[%s]: %s is a scope key type, extracted per instance rather than provided, so there is nothing to wrap\n",
				d.Iface.String(), d.Iface.String())})
		default:
			r.traceByKey[d.Iface] = d
		}
	}
}

// resolveTraced answers a request for a traced interface with its wrapper
// node, creating it on first request: the interface resolves exactly as
// it would untraced — Bind, structural search, a supplied Value, an
// Override — and the tracer provider resolves as an ordinary dependency,
// with a chain frame naming the declaration so a failure in either reads
// like any other.
//
// The wrapper is gray while its two dependencies resolve, so a tracer
// provider that itself depends on the traced interface is reported as the
// cycle it is.
func (r *resolver) resolveTraced(decl *load.TraceDecl, k graph.Key, kType types.Type, chain []chainEntry, rootPos token.Position) (*Node, bool) {
	wk := tracedKey(k)
	// Checked before the node map, the way resolveUntraced checks its own
	// failedKey first: a wrapper that failed on one dependency is still in
	// r.nodes, black, and a second consumer must get the same refusal
	// rather than the half-built node.
	if r.failedKey[wk] {
		return nil, false
	}
	if existing, ok := r.nodes[wk]; ok {
		frame := chainEntry{Key: wk, Label: tracedLabel(k), Pos: decl.Pos}
		if r.color[wk] == colorGray {
			r.diags = append(r.diags, r.cycleDiagnostic(append(append([]chainEntry{}, chain...), frame)))
			r.failedKey[wk] = true
			return nil, false
		}
		return existing, true
	}
	r.traceUsed[k] = true

	node := &Node{Key: wk, Kind: NodeTraced, Binding: "traced", Traced: &Traced{Decl: decl, IfaceType: kType}}
	r.nodes[wk] = node
	r.color[wk] = colorGray

	newChain := append(append([]chainEntry{}, chain...), chainEntry{Key: wk, Label: tracedLabel(k), Pos: decl.Pos})
	inner, innerOK := r.resolveUntraced(k, kType, newChain, rootPos)
	tracer, tracerOK := r.resolveKey(r.traceIn.ProviderKey, r.traceIn.ProviderType, newChain, rootPos)
	r.color[wk] = colorBlack
	if !innerOK || !tracerOK {
		r.failedKey[wk] = true
		return nil, false
	}

	node.Traced.Inner, node.Traced.Tracer = inner, tracer
	node.Deps = []*Node{inner, tracer}
	node.Level = max(inner.Level, tracer.Level) + 1
	r.order = append(r.order, node)
	r.traceNodes = append(r.traceNodes, node)
	return node, true
}

// tracedLabel is how a wrapper appears in a needed-by chain.
func tracedLabel(iface graph.Key) string {
	return "the servo.Trace wrapper for " + iface.String()
}

// checkTraces runs the trace checks that need the whole graph: a Trace
// nothing requests, and a wrapper around something scoped — rejected in
// this release rather than generated, since a per-key wrapper would have
// to be built and torn down inside the scope's entry.
func (r *resolver) checkTraces(decls []load.TraceDecl) {
	for i := range decls {
		d := &decls[i]
		if r.traceByKey[d.Iface] == nil || r.traceUsed[d.Iface] {
			continue // rejected at setup, or in use
		}
		r.diags = append(r.diags, r.unusedTraceDiagnostic(d))
	}
	for _, n := range r.traceNodes {
		tr := n.Traced
		switch {
		case tr.Inner.Scope != nil:
			r.diags = append(r.diags, r.scopedTraceDiagnostic(tr, tr.Inner))
		case tr.Tracer.Scope != nil:
			r.diags = append(r.diags, r.scopedTraceDiagnostic(tr, tr.Tracer))
		}
	}
}

// sortedTraceNodes orders the wrappers by declaration position, so the
// emitted types appear in the order the spec file reads rather than the
// order some root's traversal happened to reach them.
func sortedTraceNodes(nodes []*Node) []*Node {
	sorted := append([]*Node(nil), nodes...)
	sort.Slice(sorted, func(i, j int) bool {
		return graph.ComparePos(sorted[i].Traced.Decl.Pos, sorted[j].Traced.Decl.Pos) < 0
	})
	return sorted
}

func (r *resolver) missingOtelDiagnostic(d *load.TraceDecl) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: servo.Trace[%s] needs %s, which nothing in this module's build imports\n", d.Iface.String(), graph.OtelTracePackagePath)
	b.WriteString("\n  The generated wrapper depends on trace.TracerProvider, and the package\n")
	b.WriteString("  reaches the build through the provider you write for it:\n\n")
	b.WriteString("\timport sdktrace \"go.opentelemetry.io/otel/sdk/trace\"\n\n")
	b.WriteString("\tfunc NewTracerProvider() (*sdktrace.TracerProvider, error) { ... }\n\n")
	b.WriteString("  `go get go.opentelemetry.io/otel/sdk` adds the module if go.mod lacks it.\n")
	return Diagnostic{Pos: d.Pos, Message: b.String()}
}

// missingTracerProviderDiagnostic replaces the generic no-provider message
// for the one dependency every wrapper shares, since the generic one would
// list zero implementations and leave the reader to work out that the
// provider is theirs to write.
func (r *resolver) missingTracerProviderDiagnostic(chain []chainEntry, rootPos token.Position) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: no provider for %s\n", r.traceIn.ProviderKey.String())
	b.WriteString(renderChain(chain, rootPos))
	b.WriteString("\n  Every servo.Trace wrapper takes the tracer provider as an ordinary dependency —\n")
	b.WriteString("  there is no global fallback. Write a constructor whose result implements it;\n")
	b.WriteString("  the SDK's *sdktrace.TracerProvider does, and so does a type of your own that\n")
	b.WriteString("  embeds it and adds the Stop servo will call at shutdown:\n\n")
	b.WriteString("\ttype Tracer struct{ *sdktrace.TracerProvider }\n\n")
	b.WriteString("\tfunc NewTracer(cfg Config) (*Tracer, error) { ... }\n\n")
	b.WriteString("\tfunc (t *Tracer) Stop(ctx context.Context) error { return t.Shutdown(ctx) }\n")
	pos := rootPos
	if len(chain) > 0 {
		pos = chain[len(chain)-1].Pos
	}
	return Diagnostic{Pos: pos, Message: b.String()}
}

func (r *resolver) unusedTraceDiagnostic(d *load.TraceDecl) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: servo.Trace[%s]() is declared, but nothing in the graph depends on %s\n", d.Iface.String(), d.Iface.String())
	b.WriteString("\n  The wrapper is generated for the consumers of the interface, and this one\n")
	b.WriteString("  would have no consumer to hand it to.\n\n")
	b.WriteString("  Two ways out:\n")
	fmt.Fprintf(&b, "    - take it as a constructor parameter somewhere the roots reach:\n      func New(r %s) *Thing\n", d.Iface.String())
	b.WriteString("    - delete the servo.Trace declaration\n")
	return Diagnostic{Pos: d.Pos, Message: b.String()}
}

func (r *resolver) scopedTraceDiagnostic(tr *Traced, scoped *Node) Diagnostic {
	var b strings.Builder
	fmt.Fprintf(&b, "servo: servo.Trace[%s]: %s resolves to %s, which is scoped (%s), and this release does not wrap scoped instances\n",
		tr.Decl.Iface.String(), tr.Decl.Iface.String(), scoped.Key.String(), scoped.Scope.Name)
	b.WriteString("\n  A wrapper around a scoped instance would be one per key, built and torn down\n")
	b.WriteString("  with the instance inside the scope's own entry. That is well-defined, and not\n")
	b.WriteString("  yet generated. Trace the singleton interfaces the scoped type depends on\n")
	b.WriteString("  instead, or leave this one untraced.\n")
	return Diagnostic{Pos: tr.Decl.Pos, Message: b.String()}
}
