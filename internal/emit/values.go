package emit

import (
	"fmt"
	"go/types"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// valueField is one servo.Value: the exported field the caller sets on the
// generated Values struct, and the App field it is copied into.
type valueField struct {
	Name  string // exported field on Values
	Field string // App field, and the local in the constructor
	Type  string // rendered, qualified type
	Key   string // the graph key, for the doc comment
	// Nilable marks a pointer, interface, map, func or chan type, whose
	// zero value no constructor can use: NewWith refuses it.
	Nilable bool
}

// overrideField is one servo.Override whose implementation a test may
// supply per call, as a field of TestValues typed as the overridden
// interface. A nil field keeps the Override's own constructor; a set one
// is used in its place, with no constructor call and no lifecycle calls,
// exactly like a supplied value.
//
// The node then has two App fields. Built keeps the name and the concrete
// type the node always had, holding what the constructor returned — so a
// test that reaches into app.storeMock compiles as before — and is left
// nil when the test supplied the implementation. Var is of the interface
// type and holds whichever of the two the call ended up with; it is the
// node's name as far as every consumer is concerned (varName), which is
// what lets a supplied value reach them all.
type overrideField struct {
	Decl      load.BindDecl
	Node      *resolve.Node
	Name      string // exported field on TestValues, named for the interface
	IfaceType string // the interface, rendered and qualified
	Built     string // App field for the constructor's result, concrete type
	Var       string // App field and New's local, interface type
}

// planOverrideNodes finds, in the override variant, every servo.Override
// a test can supply per call. It runs before any name is allocated,
// because whether TestValues exists decides whether the constructor
// claims its parameter name.
//
// An implementation can be supplied only where nothing servo generates
// would need more from it than the interface offers. The concrete node
// must have no lifecycle at all — no Init, Run, Drain, Flush, Stop,
// Health, Ready, nor a cleanup func — since there is nothing to call those
// on when the test supplied the value; every mock generator's output
// qualifies. And every consumer must take it as the overridden interface,
// since a supplied value has only that type: a provider or a scoped
// member whose parameter is the interface, a route handler whose argument
// is, or a trace wrapper for it. Anything else — a consumer of the
// concrete type, the node acting as middleware, an extractor or the HTTP
// config, or a scope key extractor depending on it — keeps the Override
// constructor-only, exactly as it was.
func (e *emitter) planOverrideNodes() {
	if !e.testMode {
		return
	}
	for _, ov := range e.spec.Overrides {
		n := e.resolved.ByKey[ov.Concrete]
		if n == nil || !e.suppliable(n, ov) {
			continue
		}
		e.overrides = append(e.overrides, &overrideField{Decl: ov, Node: n})
	}
}

func (e *emitter) suppliable(n *resolve.Node, ov load.BindDecl) bool {
	if n.Kind != resolve.NodeProvider || n.Scope != nil || len(n.Capabilities) > 0 || n.Provider.HasCleanup {
		return false
	}
	for _, f := range e.overrides {
		if f.Node == n {
			return false // two Overrides binding one node: neither is the interface it is taken as
		}
	}
	takesIface := func(m *resolve.Node) bool {
		for i, d := range m.Deps {
			if d != n {
				continue
			}
			switch {
			case m.Kind == resolve.NodeProvider:
				if m.Provider.Params[i] != ov.Iface {
					return false
				}
			case m.Kind == resolve.NodeTraced && d == m.Traced.Inner:
				if !types.Identical(m.Traced.IfaceType, ov.IfaceType) {
					return false
				}
			case m.Kind == resolve.NodeTraced && d == m.Traced.Tracer:
				if !isTracerProvider(ov.IfaceType) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	for _, m := range e.resolved.Order {
		if !takesIface(m) {
			return false
		}
	}
	for _, s := range e.resolved.Scopes {
		for _, m := range s.Order {
			if !takesIface(m) {
				return false
			}
		}
		for _, r := range s.Roots {
			for _, d := range r.ExtractorDeps {
				if d == n {
					return false
				}
			}
		}
	}
	if h := e.resolved.HTTP; h != nil {
		if h.Config == n {
			return false
		}
		for _, u := range h.Uses {
			if u.Node == n {
				return false
			}
		}
		for _, x := range h.Extractors {
			if x.Node == n {
				return false
			}
		}
		for _, hr := range h.Routes {
			for j, arg := range hr.Args {
				if arg.Node == n && hr.Route.Deps[j] != ov.Iface {
					return false
				}
			}
		}
	}
	return true
}

// isTracerProvider reports whether t is OpenTelemetry's TracerProvider,
// the type a trace wrapper's constructor takes its tracer as.
func isTracerProvider(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == graph.OtelTracePackagePath && named.Obj().Name() == "TracerProvider"
}

// takesValues reports whether the constructor takes a Values (TestValues)
// argument: when some servo.Value is supplied, or some Override can be.
func (e *emitter) takesValues() bool {
	return len(e.resolved.Supplied) > 0 || len(e.overrides) > 0
}

// planValues names the Values struct's fields. They are exported and live
// in their own namespace — a struct the caller writes a literal for — so
// they are allocated separately from App's fields rather than sharing the
// lowercase names those use. Supplied values come first, then the
// Overrides a test can supply, each named for its interface.
func (e *emitter) planValues() {
	if !e.takesValues() {
		return
	}
	names := NewNameAllocator()
	for _, n := range e.resolved.Supplied {
		typ := nodeResultType(n)
		e.values = append(e.values, valueField{
			Name:    capitalize(names.AllocateName(baseName(typ))),
			Field:   e.varName[n.Key],
			Type:    e.qualifiedTypeString(typ),
			Key:     n.Key.String(),
			Nilable: nilable(typ),
		})
	}
	for _, f := range e.overrides {
		f.Name = capitalize(names.AllocateName(baseName(f.Decl.IfaceType)))
		f.IfaceType = e.qualifiedTypeString(f.Decl.IfaceType)
	}
}

// planOverrideFields allocates each suppliable Override's interface-typed
// App field, after every node has its own name so none of them moves to
// make room. The node's name becomes that field, so every consumer reads
// the value the call ended up with, while the concrete field keeps the
// name it always had. It is a local in New's flat scope as well, so it is
// reserved against user packages the way the undo list is.
func (e *emitter) planOverrideFields() {
	for _, f := range e.overrides {
		f.Built = e.varName[f.Node.Key]
		f.Var = e.names.AllocateName(lowerFirst(baseName(f.Decl.IfaceType)) + "Override")
		e.imports.Reserve(f.Var)
		e.varName[f.Node.Key] = f.Var
		e.overrideByNode[f.Node] = f
	}
}

// nilable reports whether t's zero value is nil in a way no constructor
// can do anything with: a pointer, interface, map, func or chan. A nil
// slice is an empty one, and stays allowed.
func nilable(t types.Type) bool {
	switch types.Unalias(t).Underlying().(type) {
	case *types.Pointer, *types.Interface, *types.Map, *types.Signature, *types.Chan:
		return true
	}
	return false
}

// valuesType names the struct the caller fills in. TestValues in the
// override variant, for the same reason TestApp exists: the two files land
// in one package and the override may resolve a different set.
func (e *emitter) valuesType() string {
	if e.testMode {
		return "TestValues"
	}
	return "Values"
}

// valuesDecl emits the Values struct, or nothing at all when the
// constructor takes none — which is what keeps every other graph's output
// byte-identical.
func (e *emitter) valuesDecl() string {
	if !e.takesValues() {
		return ""
	}
	var b strings.Builder
	switch {
	case len(e.overrides) == 0:
		fmt.Fprintf(&b, "// %s carries the values servo.Value declares: the ones the caller\n", e.valuesType())
		b.WriteString("// supplies rather than any provider builds.\n")
	case len(e.values) == 0:
		fmt.Fprintf(&b, "// %s carries, per servo.Override, an implementation to use in place\n", e.valuesType())
		b.WriteString("// of the one the Override's constructor would build.\n")
	default:
		fmt.Fprintf(&b, "// %s carries the values servo.Value declares — the ones the caller\n", e.valuesType())
		b.WriteString("// supplies rather than any provider builds — and, per servo.Override, an\n")
		b.WriteString("// implementation to use in place of the one its constructor would build.\n")
	}
	fmt.Fprintf(&b, "type %s struct {\n", e.valuesType())
	for _, v := range e.values {
		fmt.Fprintf(&b, "\t%s %s\n", v.Name, v.Type)
	}
	for _, f := range e.overrides {
		fmt.Fprintf(&b, "\t// %s, when set, is the %s every consumer gets, in place of\n", f.Name, f.IfaceType)
		fmt.Fprintf(&b, "\t// the %s the Override would build; nothing in its\n", e.qualifiedTypeString(f.Node.Provider.ResultType))
		b.WriteString("\t// lifecycle is called. Nil builds the Override as usual.\n")
		fmt.Fprintf(&b, "\t%s %s\n", f.Name, f.IfaceType)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// withConstructorName is the constructor that takes the values.
func (e *emitter) withConstructorName() string {
	return e.constructorName() + "With"
}

// valueChecks refuses, at the top of NewWith, every supplied value of a
// nilable type that the caller left nil. Nothing has been built yet, so
// there is nothing to unwind.
//
// New passes the zero Values, so a graph declaring servo.Value[*sql.DB]()
// used to hand every consumer a nil pointer from plain New, and the
// program crashed somewhere inside a constructor — or, for a
// context.Context, inside the first call that used it. The error names
// the field and the marker, by type rather than by position, so the
// committed file does not move when the spec does.
func (e *emitter) valueChecks() string {
	var b strings.Builder
	for _, v := range e.values {
		if !v.Nilable {
			continue
		}
		msg := fmt.Sprintf("servo: %s.%s is nil, but servo.Value[%s]() declares it supplied by the caller: set it in the %s passed to %s (%s supplies only zero values)",
			e.valuesType(), v.Name, v.Key, e.valuesType(), e.withConstructorName(), e.constructorName())
		fmt.Fprintf(&b, "\tif v.%s == nil {\n\t\treturn nil, %s.New(%q)\n\t}\n", v.Name, e.imports.Add("errors", "errors"), msg)
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// valueAssignments copies each supplied value out of the struct and into
// the App, at the very top of the constructor. Both the local and the
// field are emitted, so the rest of construction refers to a supplied
// value exactly the way it refers to a constructed one.
func (e *emitter) valueAssignments() string {
	var b strings.Builder
	for _, v := range e.values {
		fmt.Fprintf(&b, "\t%s := v.%s\n", v.Field, v.Name)
		fmt.Fprintf(&b, "\ta.%s = %s\n", v.Field, v.Field)
	}
	if len(e.values) > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// zeroValueDelegate emits the plain New alongside NewWith when the
// constructor takes values, so the generated method set is the documented
// one either way.
//
// It cannot do better than the zero value, and says so: for a struct of
// options that is often exactly right, a nil pointer is refused by NewWith
// rather than handed to a constructor, and an Override left nil is built
// by its own constructor. The alternative — dropping New from a graph that
// declares a value — would make the presence of a marker change the
// public API of the generated package, which is the one thing the method
// set is supposed to pin down.
func (e *emitter) zeroValueDelegate() string {
	if !e.takesValues() {
		return ""
	}
	var b strings.Builder
	if len(e.values) == 0 {
		fmt.Fprintf(&b, "// %s builds the app with every servo.Override built by its own\n", e.constructorName())
		fmt.Fprintf(&b, "// constructor. %s supplies implementations per call instead.\n", e.withConstructorName())
	} else {
		fmt.Fprintf(&b, "// %s builds the app with the zero value of every servo.Value.\n", e.constructorName())
		fmt.Fprintf(&b, "// Prefer %s: the zero value is a real value for a struct of\n", e.withConstructorName())
		b.WriteString("// options, but a nil pointer, interface, map, func or chan is refused,\n")
		b.WriteString("// so this is right only when the zero value is what you meant.\n")
	}
	fmt.Fprintf(&b, "func %s(ctx context.Context) (*%s, error) {\n", e.constructorName(), e.appType())
	fmt.Fprintf(&b, "\treturn %s(ctx, %s{})\n}\n\n", e.withConstructorName(), e.valuesType())
	return b.String()
}

// writeOverrideConstruction builds a suppliable Override's node: the
// TestValues field when the test set it, the Override's own constructor
// otherwise. Either way the interface-typed local and field end up
// holding it, and only the constructor's result is stored in the concrete
// field. The node has no lifecycle (planOverrideNodes made sure), so
// nothing has to be undone for it and nothing later calls into it.
func (e *emitter) writeOverrideConstruction(b *strings.Builder, index int, f *overrideField) {
	n := f.Node
	args := make([]string, len(n.Deps))
	for i, dep := range n.Deps {
		args[i] = e.appArg(dep)
	}
	call := fmt.Sprintf("%s(%s)", e.qualifiedFuncString(n.Provider), strings.Join(args, ", "))

	fmt.Fprintf(b, "\tvar %s %s\n", f.Var, f.IfaceType)
	fmt.Fprintf(b, "\tif v.%s != nil {\n\t\t%s = v.%s\n\t} else {\n", f.Name, f.Var, f.Name)
	if n.Provider.HasError {
		fmt.Fprintf(b, "\t\t%s, err := %s\n", f.Built, call)
		b.WriteString("\t\tif err != nil {\n")
		e.writeConstructionRollback(b, index)
		b.WriteString("\t\t\treturn nil, err\n\t\t}\n")
	} else {
		fmt.Fprintf(b, "\t\t%s := %s\n", f.Built, call)
	}
	fmt.Fprintf(b, "\t\ta.%s = %s\n\t\t%s = %s\n\t}\n", f.Built, f.Built, f.Var, f.Built)
	fmt.Fprintf(b, "\ta.%s = %s\n\n", f.Var, f.Var)
}
