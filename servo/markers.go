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

// Value declares that T is supplied by the caller rather than built from a
// provider — a parsed flag set, a version string injected at link time, a
// *sql.DB opened by a test harness, a fixed clock.
//
// Everything else in the graph is resolved by finding the one function
// that produces it. A value that only exists once the process is already
// running has no such function, and the alternative servo used to leave
// open — a package-level var in main, read back by a small provider beside
// it — is the global-lookup pattern this version exists to remove.
//
// Declaring one changes the generated API additively. The injector keeps
// New(ctx), and gains:
//
//	type Values struct{ Flags config.Flags }
//	func NewWith(ctx context.Context, v Values) (*App, error)
//
// NewWith is the one to call. New still exists so the generated method set
// is the documented one either way, but it can only pass the zero value of
// every declared type — which is a real value for a struct of options and
// a nil pointer for a *sql.DB, so reach for it only when the zero value is
// the value you meant.
//
// T is matched by type, exactly as a constructor parameter is, and beats
// any provider that also produces it: the spec file said so. A T nothing
// in the graph depends on is a generate-time diagnostic rather than an
// unused struct field.
func Value[T any]() Marker {
	panic("servo: Value executed at runtime — run `servo generate`")
}

// ConfigFile declares the config file this injector's //servo:config types
// may read, in addition to the environment:
//
//	servo.Build(
//	    servo.Root[*api.Server](),
//	    servo.ConfigFile("config.yaml"),
//	)
//
// The path must be a string literal ending in .json, .yaml, .yml, or .toml
// — it is read as syntax, and the extension decides which decoder the
// generated code carries, so an env-only app never gains a yaml dependency
// and a yaml app never gains a toml one. At runtime the path can be
// overridden with the CONFIG_FILE environment variable (same extension
// family only). A missing file at the declared default path is not an
// error — every setting can still arrive from the environment — but a
// path set explicitly via CONFIG_FILE must exist.
//
// Precedence per setting is default, then file, then environment: the
// environment always wins, so an operator can override any file value in a
// deployment without editing it.
//
// With no ConfigFile declared, //servo:config loaders read only the
// environment, and their generated signature takes no arguments at all. A
// module whose injectors share a config type must therefore agree: all of
// them declare ConfigFile, or none — `servo generate` refuses the mix,
// since one companion loader cannot have two signatures.
func ConfigFile(path string) Marker {
	panic("servo: ConfigFile executed at runtime — run `servo generate`")
}

// Include splices another function's markers into this Build call, so a
// set of Bind/Override/Scoped declarations shared by several injectors is
// written once:
//
//	// wiring/wiring.go, //go:build servoinject
//	func Shared() []servo.Marker {
//	    return []servo.Marker{
//	        servo.Bind[store.Store, *postgres.DB](),
//	        servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(time.Minute)),
//	    }
//	}
//
//	// cmd/api/spec.go
//	servo.Build(
//	    servo.Include(wiring.Shared),
//	    servo.Root[*api.Server](),
//	)
//
// The argument names the function; it is never called. `servo generate`
// reads the referenced declaration's returned slice literal as syntax, the
// same way it reads Build's own argument list, and the file it lives in
// must carry the servoinject build tag for the same reason a spec file
// does. An included function may itself Include another; a cycle is a
// diagnostic, not a hang.
//
// Markers an Include brings in are ordered before the ones written after
// it, so a Bind in the spec file still overrides a shared one — the local
// file has the last word, which is the only ordering that makes a shared
// set worth having.
func Include(func() []Marker) Marker {
	panic("servo: Include executed at runtime — run `servo generate`")
}
