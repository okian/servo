package servo

import (
	"errors"
	"time"
)

// ScopeOption configures one servo.Scoped declaration. Like every other
// marker, it is read as syntax by `servo generate` and never executed —
// Linger and Max panic if they ever actually run, for the same reason
// Build/Root/Bind do.
type ScopeOption struct{}

// Scoped declares that T is a keyed, refcounted, lifecycle-managed
// instance rather than a singleton.
//
// T must declare a ScopeKey method (see the doc comment on ErrNoScopeKey
// for its shape); everything T transitively depends on that also depends
// on the key type becomes part of the same scope, constructed and torn
// down together.
//
// I is an interface declared in the user's own package that the generated
// accessor satisfies:
//
//	type Rooms interface {
//	    Acquire(ctx context.Context) (*Room, func(), error)
//	}
//
// It exists because `servo generate` cannot emit a type into the user's
// package, so without a user-declared interface there is nothing for a
// consumer's constructor to depend on. I may declare Acquire, Stats
// (returning ScopeStats), or both, and nothing else — any other method is
// a generate-time diagnostic rather than a compile error in generated
// code.
func Scoped[T, I any](...ScopeOption) Marker {
	panic("servo: Scoped executed at runtime — run `servo generate`")
}

// Linger sets how long a scope keeps an instance alive after its last
// holder releases it, before draining, stopping and evicting it.
//
// Without a linger window the reference count of a short request handler
// goes 0→1→0 per request and the instance is rebuilt every time, losing
// whatever in-memory state made it worth sharing. Linger(0) is therefore a
// deliberate policy — die with the last holder — not a default worth
// falling into; when a Scoped declaration omits Linger entirely it gets
// DefaultLinger.
func Linger(time.Duration) ScopeOption {
	panic("servo: Linger executed at runtime — run `servo generate`")
}

// Max caps how many distinct keys a scope may hold live instances for at
// once. Acquiring a new key beyond the cap returns ErrScopeFull rather
// than allocating.
//
// Scope keys usually come from user input — a room name, a tenant ID, a
// region — so an uncapped scope is an unauthenticated allocation
// primitive. A declaration that omits Max gets DefaultMax.
//
// The cap counts live instances, which includes ones that have already
// been evicted but are still draining and stopping — they still hold their
// memory. Under Max pressure with a slow Drain, that means re-acquiring a
// key which is merely finishing its teardown can be refused with
// ErrScopeFull until it completes.
func Max(int) ScopeOption {
	panic("servo: Max executed at runtime — run `servo generate`")
}

// DefaultLinger and DefaultMax are the values `servo generate` bakes into
// a scope whose declaration omits Linger or Max. They are generate-time
// constants read by the generator, not runtime knobs: changing them here
// changes what the *next* generation emits, and has no effect on already
// generated code.
const (
	DefaultLinger = 30 * time.Second
	DefaultMax    = 10_000
)

// Errors returned by a generated scope accessor's Acquire. A component
// implementing ScopeKey returns ErrNoScopeKey itself; the other three are
// returned by generated code.
var (
	// ErrNoScopeKey reports that the context carries no scope key. It is
	// returned by the user's own ScopeKey method, whose shape is:
	//
	//	func (*Room) ScopeKey(ctx context.Context, deps ...) (RoomKey, error)
	//
	// The receiver must be unnamed: generated code calls the method on a
	// typed nil, because it needs the key before it can choose an
	// instance. `servo generate` and `servo-vet` both reject a named
	// receiver. A blank `_` is accepted too, but staticcheck's ST1006
	// flags it and asks for the form above.
	//
	// The error result is not optional. Without it a missing key becomes
	// the zero value of the key type, and every keyless caller silently
	// shares one instance — a cross-tenant leak with no symptom.
	ErrNoScopeKey = errors.New("servo: no scope key in context")

	// ErrNoLifetime reports that Acquire was handed a context that can
	// never be done — context.Background, context.TODO, or a
	// context.WithoutCancel of either.
	//
	// A scope releases a reference when the caller calls the returned
	// closure, and, as a backstop, when the acquiring context ends. A
	// context with no Done channel disables that backstop, so a caller
	// who forgets the closure pins the instance for the life of the
	// process. Refusing the acquire is the only way to say so.
	ErrNoLifetime = errors.New("servo: context has no Done channel — a scoped instance needs a cancellable context so a forgotten release still frees it")

	// ErrScopeFull reports that a scope already holds Max live instances
	// and the requested key is not one of them. "Live" includes instances
	// that have been evicted but are still tearing down — see Max.
	ErrScopeFull = errors.New("servo: scope is at its Max live-instance cap")

	// ErrScopeClosed reports that the application is shutting down and the
	// scope has stopped accepting acquires. Every instance it held is
	// being, or has been, torn down.
	ErrScopeClosed = errors.New("servo: scope is shut down")
)

// ScopeStats is a point-in-time view of one scope, returned by the
// generated accessor's Stats method. It is test- and debug-facing: it
// exists so a test can assert "no instances survived" without reaching
// into generated internals. Wiring it to Prometheus is the caller's job —
// servo exports no metrics itself.
//
// Live and Refs are sampled independently, a nanosecond apart, under a
// scope that other goroutines are still using. Treat them as a snapshot
// of a moving system, not as two halves of one atomic read.
type ScopeStats struct {
	// Live is how many instances exist: keys inside their linger window
	// with no holders left are counted, and so is an instance whose
	// teardown is still running. Waiting for Live to reach zero is
	// therefore a valid way to wait for a scope to go quiet.
	Live int `json:"live"`
	// Refs is how many outstanding references exist across every live
	// instance — one per Acquire whose release has not yet run.
	Refs int `json:"refs"`
	// Acquires and Evictions are monotonic totals since the scope was
	// created, not current values. An eviction is counted once its
	// instance has finished draining and stopping, not when it was
	// chosen for eviction.
	Acquires  uint64 `json:"acquires"`
	Evictions uint64 `json:"evictions"`
	// Failures is how many of those evictions did not come out clean —
	// a Drain or Stop that returned an error, or one that overran its
	// budget and was abandoned.
	//
	// It exists because an instance evicted mid-life has no Report to
	// appear in: Shutdown is not running, and nobody is waiting on that
	// teardown. Without a counter, a component that consistently fails to
	// stop would leave no trace anywhere. Watch it the way you would watch
	// any error rate; the detail of *which* phase failed is not recovered.
	Failures uint64 `json:"failures"`
}

// LingerOverride replaces every generated scope's declared linger window
// when it is non-negative. It is a package var, not a config file, for the
// same reason DefaultStopBudget is one: servotest.Linger sets it so a test
// can force the eviction-racing-acquire boundary without a slow suite.
//
// Generated code reads it exactly once per scope, inside New. Tests using
// servotest.Linger must therefore set it before calling New, and must not
// run in parallel with each other or with tests that depend on a scope's
// real declared window.
var LingerOverride time.Duration = -1

// LingerWindow returns declared unless LingerOverride is active. Generated
// code calls it once per scope, in New.
func LingerWindow(declared time.Duration) time.Duration {
	if LingerOverride >= 0 {
		return LingerOverride
	}
	return declared
}
