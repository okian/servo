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

// HTTP declares that this injector serves the module's //servo: route
// directives: `servo generate` emits an HTTP server that registers every
// directive handler, decodes requests into their typed structs, and calls
// them with graph-resolved dependencies. The server's listen address, TLS
// and limits come from a *servo.HTTPConfig node the user provides. Options
// declare extra listener groups and attach middleware — see Group, Use and
// Route. At most one HTTP() per Build.
func HTTP(...HTTPOption) Marker {
	panic("servo: HTTP executed at runtime — run `servo generate`")
}

// HTTPOption is the opaque return type of Group, Use and Route — read as
// syntax inside servo.HTTP's argument list, never executed, for the same
// reason ScopeOption exists.
type HTTPOption struct{}

// Group, inside servo.HTTP(...), declares a named listener group: routes
// carrying the name as their directive's trailing token
// (`//servo:get /healthz telemetry`) are served on the listener
// HTTPConfig.Groups[name] describes. Inside servo.Use(...), it selects the
// group the middleware wraps. The default group needs no declaration;
// "default" names it in a Use selector. The name must be a constant string
// matching [A-Za-z0-9_-]+.
func Group(name string) HTTPOption {
	panic("servo: Group executed at runtime — run `servo generate`")
}

// Use attaches middleware: T is an ordinary graph node whose
//
//	Middleware(next http.Handler) http.Handler
//
// method wraps the emitted server. With no selector it wraps every group;
// Group selectors wrap one group's whole mux; Route selectors wrap single
// routes. One Use selects groups or routes, never both. Declaration order
// is outermost-first, stacked server → group → route.
func Use[T any](...HTTPOption) HTTPOption {
	panic("servo: Use executed at runtime — run `servo generate`")
}

// Route, inside servo.Use(...), selects one route by its full
// "METHOD /pattern" spelling — a constant string that must exactly match a
// served route, checked at generate time.
func Route(pattern string) HTTPOption {
	panic("servo: Route executed at runtime — run `servo generate`")
}

// Extract declares T as an extractor: its
//
//	Extract(r *http.Request) (V, error)
//
// method makes every //servo: handler parameter of type V a per-request
// value produced from the request, instead of a graph dependency. An
// extraction error short-circuits through the same status contract as a
// handler error, so servo.Status.UNAUTHORIZED.Wrap(err) is a 401 before
// the handler runs.
func Extract[T any]() Marker {
	panic("servo: Extract executed at runtime — run `servo generate`")
}
