// Package servo provides the marker functions read by `servo generate` and
// the small runtime shared by all generated injectors.
package servo

// Marker is the opaque return type of Root, Bind, Override, and Scoped. It
// carries no data; `servo generate` reads calls to these functions as
// syntax inside a Build(...) argument list and never executes them.
type Marker struct{}

// Build declares an injector's roots, explicit bindings, and scopes. Calls to it are
// read as syntax by `servo generate`, in a file carrying the servoinject
// build tag that is excluded from the compiled binary. If Build ever runs,
// the tag was missing or generation was skipped — panic rather than
// silently returning a nil app.
func Build(...Marker) {
	panic("servo: Build executed at runtime — run `servo generate`")
}

// Root declares T as a root of the object graph: T and everything it
// transitively depends on is constructed; unreachable candidates are never
// emitted.
func Root[T any]() Marker {
	panic("servo: Root executed at runtime — run `servo generate`")
}

// Bind declares that concrete type C satisfies interface I wherever I is
// requested, resolving a binding that would otherwise be ambiguous or
// missing.
func Bind[I, C any]() Marker {
	panic("servo: Bind executed at runtime — run `servo generate`")
}

// Override declares a test-only replacement for I, used only by
// `servo generate` when emitting NewTestApp alongside New.
func Override[I, C any]() Marker {
	panic("servo: Override executed at runtime — run `servo generate`")
}
