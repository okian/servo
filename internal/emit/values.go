package emit

import (
	"fmt"
	"strings"
)

// valueField is one servo.Value: the exported field the caller sets on the
// generated Values struct, and the App field it is copied into.
type valueField struct {
	Name  string // exported field on Values
	Field string // App field, and the local in the constructor
	Type  string // rendered, qualified type
	Key   string // the graph key, for the doc comment
}

// planValues names the Values struct's fields. They are exported and live
// in their own namespace — a struct the caller writes a literal for — so
// they are allocated separately from App's fields rather than sharing the
// lowercase names those use.
func (e *emitter) planValues() {
	if len(e.resolved.Supplied) == 0 {
		return
	}
	names := NewNameAllocator()
	for _, n := range e.resolved.Supplied {
		field := e.varName[n.Key]
		e.values = append(e.values, valueField{
			Name:  capitalize(names.AllocateName(baseName(nodeResultType(n)))),
			Field: field,
			Type:  e.qualifiedTypeString(nodeResultType(n)),
			Key:   n.Key.String(),
		})
	}
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

// valuesDecl emits the Values struct, or nothing at all when no
// servo.Value is declared — which is what keeps every existing graph's
// output byte-identical.
func (e *emitter) valuesDecl() string {
	if len(e.values) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s carries the values servo.Value declares: the ones the caller\n", e.valuesType())
	b.WriteString("// supplies rather than any provider builds.\n")
	fmt.Fprintf(&b, "type %s struct {\n", e.valuesType())
	for _, v := range e.values {
		fmt.Fprintf(&b, "\t%s %s\n", v.Name, v.Type)
	}
	b.WriteString("}\n\n")
	return b.String()
}

// withConstructorName is the constructor that takes the values.
func (e *emitter) withConstructorName() string {
	return e.constructorName() + "With"
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

// zeroValueDelegate emits the plain New alongside NewWith when values are
// declared, so the generated method set is the documented one either way.
//
// It cannot do better than the zero value, and says so: for a struct of
// options that is often exactly right, and for a *sql.DB it is nil. The
// alternative — dropping New from a graph that declares a value — would
// make the presence of a marker change the public API of the generated
// package, which is the one thing the method set is supposed to pin down.
func (e *emitter) zeroValueDelegate() string {
	if len(e.values) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s builds the app with the zero value of every servo.Value.\n", e.constructorName())
	fmt.Fprintf(&b, "// Prefer %s: the zero value is a real value for a struct of\n", e.withConstructorName())
	b.WriteString("// options and a nil pointer for anything else, so this is right only when\n")
	b.WriteString("// the zero value is what you meant.\n")
	fmt.Fprintf(&b, "func %s(ctx context.Context) (*%s, error) {\n", e.constructorName(), e.appType())
	fmt.Fprintf(&b, "\treturn %s(ctx, %s{})\n}\n\n", e.withConstructorName(), e.valuesType())
	return b.String()
}
