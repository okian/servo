package emit

import (
	"fmt"
	"go/types"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/resolve"
)

// tracedEmit is one servo.Trace wrapper plus the identifiers the generated
// file needs for it, allocated once up front like a scope's or a server's.
type tracedEmit struct {
	N        *resolve.Node
	TypeName string // tracedStore
	NewFunc  string // newTracedStore
}

// tracedReservedIdents are the receiver, parameters and locals every
// emitted wrapper names outright. A user package allowed to claim one of
// them would shadow it exactly where generated code qualifies a type with
// it — the same rule scopeReservedIdents and httpReservedIdents follow.
var tracedReservedIdents = []string{"t", "tp", "inner", "span", "ctx", "err"}

// planTraced allocates every wrapper identifier and registers the two
// OpenTelemetry imports the wrappers hard-code. Nothing here runs for a
// graph without a Trace, which is what keeps every such graph's output
// byte-identical.
func (e *emitter) planTraced() {
	if len(e.resolved.Traced) == 0 {
		return
	}
	e.traceIdent = e.imports.Add(graph.OtelTracePackagePath, "trace")
	e.codesIdent = e.imports.Add(graph.OtelCodesPackagePath, "codes")
	e.imports.Reserve(tracedReservedIdents...)
	for _, n := range e.resolved.Traced {
		if len(n.Traced.Decl.Expected) > 0 {
			e.imports.Add("errors", "errors")
			break
		}
	}
	for _, n := range e.resolved.Traced {
		base := "traced" + capitalize(tracedTypeName(n.Traced.IfaceType))
		te := &tracedEmit{N: n}
		te.TypeName = e.types.AllocateName(e.testPrefixed(base))
		te.NewFunc = e.types.AllocateName("new" + capitalize(e.testPrefixed(base)))
		e.traced = append(e.traced, te)
		e.tracedByNode[n] = te
	}
}

// tracedTypeName is the interface's own name, which the loader guarantees
// it has: an interface literal is refused at the declaration.
func tracedTypeName(t types.Type) string {
	if named, ok := types.Unalias(t).(*types.Named); ok {
		return named.Obj().Name() + typeArgsWord(named)
	}
	return "Iface"
}

// tracedDecls emits every wrapper: the type, its constructor, and one
// method per interface method.
func (e *emitter) tracedDecls() string {
	var b strings.Builder
	for _, te := range e.traced {
		e.writeTraced(&b, te)
	}
	return b.String()
}

func (e *emitter) writeTraced(b *strings.Builder, te *tracedEmit) {
	tr := te.N.Traced
	named := types.Unalias(tr.IfaceType).(*types.Named)
	iface := named.Underlying().(*types.Interface)
	ifaceStr := e.qualifiedTypeString(tr.IfaceType)
	// The span name is the package's short name, which is what a reader
	// of a trace recognises; the instrumentation scope carries the full
	// import path, so two packages with one short name stay apart.
	spanPrefix := named.Obj().Name()
	scopeName := named.Obj().Name()
	if pkg := named.Obj().Pkg(); pkg != nil {
		spanPrefix = pkg.Name() + "." + spanPrefix
		scopeName = pkg.Path()
	}
	if named.TypeArgs().Len() > 0 {
		// Two instantiations of one generic interface are two traced
		// nodes; without the arguments both would emit one span name and
		// a trace could not tell Store[User] from Store[Order]. Short
		// package names, like the prefix itself.
		spanPrefix = types.TypeString(named, func(p *types.Package) string { return p.Name() })
	}

	fmt.Fprintf(b, "// %s is the OpenTelemetry wrapper servo.Trace[%s] declares:\n", te.TypeName, ifaceStr)
	b.WriteString("// every method whose first parameter is a\n")
	fmt.Fprintf(b, "// context.Context runs inside a span named %q, the span's\n", spanPrefix+"/<Method>")
	b.WriteString("// context is what the implementation receives, and a non-nil error marks the\n")
	if len(tr.Decl.Expected) > 0 {
		names := make([]string, len(tr.Decl.Expected))
		for i, ex := range tr.Decl.Expected {
			names[i] = e.expectedRef(ex.Obj)
		}
		fmt.Fprintf(b, "// span — except %s, declared expected. Methods without a\n", strings.Join(names, ", "))
	} else {
		b.WriteString("// span. Methods without a\n")
	}
	b.WriteString("// context are delegated untouched: there is no context to carry a span. The\n")
	b.WriteString("// wrapper has no lifecycle of its own; the implementation keeps its own.\n")
	fmt.Fprintf(b, "type %s struct {\n\tinner %s\n\ttracer %s.Tracer\n}\n\n", te.TypeName, ifaceStr, e.traceIdent)

	fmt.Fprintf(b, "func %s(inner %s, tp %s.TracerProvider) *%s {\n", te.NewFunc, ifaceStr, e.traceIdent, te.TypeName)
	fmt.Fprintf(b, "\treturn &%s{inner: inner, tracer: tp.Tracer(%q)}\n}\n\n", te.TypeName, scopeName)

	for i := 0; i < iface.NumMethods(); i++ {
		e.writeTracedMethod(b, te, iface.Method(i), spanPrefix)
	}
}

// expectedRef renders one Expected error the way the generated errors.Is
// check spells it: bare inside the injector's own package, qualified
// otherwise.
func (e *emitter) expectedRef(obj types.Object) string {
	if ident := e.pkgIdent(obj.Pkg()); ident != "" {
		return ident + "." + obj.Name()
	}
	return obj.Name()
}

func (e *emitter) writeTracedMethod(b *strings.Builder, te *tracedEmit, m *types.Func, spanPrefix string) {
	sig := m.Type().(*types.Signature)
	params, results := sig.Params(), sig.Results()
	traced := params.Len() > 0 && graph.IsContextType(params.At(0).Type())
	returnsErr := results.Len() > 0 && graph.IsErrorType(results.At(results.Len()-1).Type())

	// Parameter names come from the interface where they are usable, so
	// the wrapper reads like the interface it implements. One that is
	// blank, missing, or spelled like an identifier the method body
	// hard-codes falls back to a positional name — the body references
	// the receiver, the span, the error, the result locals, and the
	// packages the status and the expected errors are qualified with, and
	// a parameter shadowing any of those would change what the body means.
	// Signature types are safe either way: a parameter's scope starts at
	// the body.
	taken := NewNameAllocator()
	for _, ident := range []string{"t", "ctx", "span", "err", e.codesIdent, "errors"} {
		taken.AllocateName(ident)
	}
	for _, ex := range te.N.Traced.Decl.Expected {
		if ident := e.pkgIdent(ex.Obj.Pkg()); ident != "" {
			taken.AllocateName(ident)
		}
	}
	for i := 0; i < results.Len(); i++ {
		taken.AllocateName(fmt.Sprintf("r%d", i))
	}
	names := make([]string, params.Len())
	for i := range names {
		switch {
		case i == 0 && traced:
			names[i] = "ctx"
		default:
			name := params.At(i).Name()
			if name == "" || name == "_" || !taken.Free(name) {
				name = fmt.Sprintf("p%d", i)
			}
			names[i] = taken.AllocateName(name)
		}
	}

	paramDecls := make([]string, params.Len())
	args := make([]string, params.Len())
	for i := range paramDecls {
		typ := e.qualifiedTypeString(params.At(i).Type())
		args[i] = names[i]
		if sig.Variadic() && i == params.Len()-1 {
			typ = "..." + e.qualifiedTypeString(params.At(i).Type().(*types.Slice).Elem())
			args[i] += "..."
		}
		paramDecls[i] = names[i] + " " + typ
	}
	resultTypes := make([]string, results.Len())
	for i := range resultTypes {
		resultTypes[i] = e.qualifiedTypeString(results.At(i).Type())
	}
	resultDecl := ""
	switch len(resultTypes) {
	case 0:
	case 1:
		resultDecl = " " + resultTypes[0]
	default:
		resultDecl = " (" + strings.Join(resultTypes, ", ") + ")"
	}

	fmt.Fprintf(b, "func (t *%s) %s(%s)%s {\n", te.TypeName, m.Name(), strings.Join(paramDecls, ", "), resultDecl)
	call := fmt.Sprintf("t.inner.%s(%s)", m.Name(), strings.Join(args, ", "))

	switch {
	case !traced:
		if results.Len() == 0 {
			fmt.Fprintf(b, "\t%s\n", call)
		} else {
			fmt.Fprintf(b, "\treturn %s\n", call)
		}
	default:
		fmt.Fprintf(b, "\tctx, span := t.tracer.Start(ctx, %q)\n", spanPrefix+"/"+m.Name())
		b.WriteString("\tdefer span.End()\n")
		switch {
		case !returnsErr && results.Len() == 0:
			fmt.Fprintf(b, "\t%s\n", call)
		case !returnsErr:
			fmt.Fprintf(b, "\treturn %s\n", call)
		default:
			locals := make([]string, results.Len())
			for i := range locals {
				locals[i] = fmt.Sprintf("r%d", i)
			}
			locals[len(locals)-1] = "err"
			fmt.Fprintf(b, "\t%s := %s\n", strings.Join(locals, ", "), call)
			cond := "err != nil"
			for _, ex := range te.N.Traced.Decl.Expected {
				cond += fmt.Sprintf(" && !errors.Is(err, %s)", e.expectedRef(ex.Obj))
			}
			fmt.Fprintf(b, "\tif %s {\n", cond)
			b.WriteString("\t\tspan.RecordError(err)\n")
			fmt.Fprintf(b, "\t\tspan.SetStatus(%s.Error, err.Error())\n", e.codesIdent)
			b.WriteString("\t}\n")
			fmt.Fprintf(b, "\treturn %s\n", strings.Join(locals, ", "))
		}
	}
	b.WriteString("}\n\n")
}

// writeTracedConstruction emits a wrapper's one construction line: no
// error, no cleanup, nothing to roll back.
func (e *emitter) writeTracedConstruction(b *strings.Builder, n *resolve.Node) {
	name := e.varName[n.Key]
	te := e.tracedByNode[n]
	fmt.Fprintf(b, "\t%s := %s(%s, %s)\n", name, te.NewFunc, e.appArg(n.Traced.Inner), e.appArg(n.Traced.Tracer))
	fmt.Fprintf(b, "\ta.%s = %s\n\n", name, name)
}
