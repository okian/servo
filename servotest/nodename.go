package servotest

import (
	"reflect"
	"strconv"
	"strings"
)

// NodeName returns the name servo gives T's node: the string a generated
// App's Report, StartupReport and Graph use for it, and so the string
// AssertStopOrder and AssertInitOrder match.
//
//	servotest.AssertStopOrder(t, rec,
//		servotest.NodeName[*api.Server](),
//		servotest.NodeName[*postgres.DB](),
//	)
//
// Nodes are named by type, with full import paths, and a pointer keeps
// its star: *api.Server in a module example.com/app is
// "*example.com/app/api.Server", never the "*api.Server" source code
// spells. Writing the short form by hand is the mistake this exists to
// prevent — it never matches, and the assertion fails on an order that
// was right.
//
// It is computed at run time, from T itself, so a renamed or moved type
// keeps its assertion in step without an edit. That covers what a node
// can be — a defined type, a pointer to one, an instantiated generic
// type, whose type arguments are spelled with full import paths too, or
// an interface literal — except for three things run time does not keep,
// which servo spells as the source does:
//
//   - Parameter and result names. A func or interface literal, wherever
//     it sits in T, comes out without them: a node of type
//     interface{ Get(key string) string } is "interface{Get(string) string}"
//     here, and "interface{Get(key string) string}" to servo.
//   - Embedded interfaces. Run time flattens an interface literal's method
//     set, so interface{ io.Reader; Close() error } comes out with Read's
//     signature written out where servo writes io.Reader.
//   - byte and rune. Run time knows them only as uint8 and int32, so the
//     type argument of a *cache.Cache[[]byte] comes out as []uint8.
//
// A name of its own is the cure for all three: declare the interface, or
// the type argument, as a defined type — type ReadCloser interface{
// io.Reader; Close() error }, type Payload []byte — and NodeName and servo
// agree on that name. For byte and rune, spelling uint8 and int32 where
// the node's type is written does as well.
//
// And a binary built from package main names that package "main", where
// servo uses its import path; under go test the package being tested
// carries its import path, so a test's own types are named as servo names
// them.
func NodeName[T any]() string {
	return typeName(reflect.TypeFor[T]())
}

// typeName renders t the way go/types renders it with full import paths,
// which is how the generator names a node.
func typeName(t reflect.Type) string {
	if t.Name() != "" {
		if t.PkgPath() == "" {
			return t.Name() // a predeclared type: error, int, string
		}
		return t.PkgPath() + "." + typeArgs(t.Name())
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + typeName(t.Elem())
	case reflect.Slice:
		return "[]" + typeName(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + typeName(t.Elem())
	case reflect.Map:
		return "map[" + typeName(t.Key()) + "]" + typeName(t.Elem())
	case reflect.Chan:
		switch t.ChanDir() {
		case reflect.RecvDir:
			return "<-chan " + typeName(t.Elem())
		case reflect.SendDir:
			return "chan<- " + typeName(t.Elem())
		}
		// A <- binds to the leftmost chan it can, so chan <-chan T would
		// read as chan<- (chan T): go/types parenthesizes an unnamed
		// receive-only element of a bidirectional channel, and so does
		// the generator's name for it.
		if e := t.Elem(); e.Kind() == reflect.Chan && e.Name() == "" && e.ChanDir() == reflect.RecvDir {
			return "chan (" + typeName(e) + ")"
		}
		return "chan " + typeName(t.Elem())
	case reflect.Func:
		return "func" + signature(t)
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return "any"
		}
		methods := make([]string, t.NumMethod())
		for i := range t.NumMethod() {
			m := t.Method(i)
			methods[i] = m.Name + signature(m.Type)
		}
		return "interface{" + strings.Join(methods, "; ") + "}"
	case reflect.Struct:
		fields := make([]string, t.NumField())
		for i := range t.NumField() {
			f := t.Field(i)
			fields[i] = typeName(f.Type)
			if !f.Anonymous {
				fields[i] = f.Name + " " + fields[i]
			}
			if f.Tag != "" {
				fields[i] += " " + strconv.Quote(string(f.Tag))
			}
		}
		return "struct{" + strings.Join(fields, "; ") + "}"
	}
	return t.String()
}

// signature renders a func type's parameters and results, go/types style.
func signature(t reflect.Type) string {
	in := make([]string, t.NumIn())
	for i := range t.NumIn() {
		if t.IsVariadic() && i == t.NumIn()-1 {
			in[i] = "..." + typeName(t.In(i).Elem())
			continue
		}
		in[i] = typeName(t.In(i))
	}
	s := "(" + strings.Join(in, ", ") + ")"
	switch t.NumOut() {
	case 0:
		return s
	case 1:
		return s + " " + typeName(t.Out(0))
	}
	out := make([]string, t.NumOut())
	for i := range t.NumOut() {
		out[i] = typeName(t.Out(i))
	}
	return s + " (" + strings.Join(out, ", ") + ")"
}

// typeArgs respaces an instantiated generic type's name: the runtime
// spells the type-argument list `Pair[string,int]`, go/types — and so the
// generator — `Pair[string, int]`, and the arguments' own literal types
// `struct { A int }` where go/types writes `struct{A int}`. The arguments
// already carry full import paths, which is the part that matters most.
func typeArgs(name string) string {
	open := strings.IndexByte(name, '[')
	if open < 0 {
		return name
	}
	var b strings.Builder
	b.WriteString(name[:open])
	args := name[open:]
	for i := 0; i < len(args); i++ {
		b.WriteByte(args[i])
		if args[i] == ',' && i+1 < len(args) && args[i+1] != ' ' {
			b.WriteByte(' ')
		}
	}
	return strings.NewReplacer(
		"interface {}", "any",
		"struct {}", "struct{}",
		"struct { ", "struct{",
		"interface { ", "interface{",
		" }", "}",
	).Replace(b.String())
}
