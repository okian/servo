# servo package

```go
import "github.com/okian/servo/v3/servo"
```

**Who this is for:** anyone writing a `main` around a generated App, reading a generated file's
calls into the runtime, or handling a `Report` in their own code.

This package is two unrelated halves. The **markers** (`Build`, `Root`, `Bind`, `Override`,
`Scoped`, `HTTP`, `Value`, `Include`, `ConfigFile`, and the two scope options) are read as syntax by
`servo generate` and panic if they ever execute. The **runtime** is what generated code — and, for
the HTTP types, your handlers, and for [`Main`](#main), your `main` — actually calls at run time.
It imports nothing outside the standard
library, and neither it nor any generated output imports `reflect` for servo's own wiring
(`encoding/json`, used by emitted HTTP servers, reflects internally like it does for everyone).
Helpers that only generated code calls live in a separate package,
[`servoimpl`](#servoimpl), which is not for direct use.

Your components never import this package. Capability interfaces are satisfied structurally, by
having the method.

## Index

| Identifier | Kind | |
| --- | --- | --- |
| [`Build`](#build) | func | Declares an injector (marker) |
| [`Root`](#root) | func | Declares a graph root (marker) |
| [`Bind`](#bind) | func | Binds an interface to a concrete type (marker) |
| [`Override`](#override) | func | Test-only binding (marker) |
| [`Scoped`](#scoped) | func | Declares a keyed, refcounted instance (marker) |
| [`HTTP`](#http) | func | Opts the injector into serving `//servo:` routes (marker) |
| [`Group`](#http), [`Use`](#http), [`Route`](#http) | funcs | Listener groups and middleware attachment (markers) |
| [`Extract`](#http) | func | Declares a per-request parameter extractor (marker) |
| [`HTTPOption`](#http) | type | `Group`/`Use`/`Route`'s opaque return type |
| [`Value`](#value) | func | Declares a type the caller supplies (marker) |
| [`Include`](#include) | func | Splices another function's marker list in (marker) |
| [`ConfigFile`](#configfile) | func | Declares the config file `//servo:config` types read (marker) |
| [`Trace`](#trace) | func | Declares a generated OpenTelemetry wrapper for an interface (marker) |
| [`Expected`](#trace) | func | Names an error the wrapper leaves off the span (marker) |
| [`TraceOption`](#trace) | type | `Expected`'s opaque return type |
| [`Linger`](#linger-and-max), [`Max`](#linger-and-max) | funcs | Scope policy (markers) |
| [`Marker`](#marker) | type | The markers' opaque return type |
| [`ScopeOption`](#scopeoption) | type | `Linger`/`Max`'s opaque return type |
| [`DefaultLinger`](#linger-and-max), [`DefaultMax`](#linger-and-max) | consts | What an omitted scope option becomes |
| [`ErrNoScopeKey`](#scope-errors) … [`ErrScopeClosed`](#scope-errors) | vars | The four scope errors |
| [`ScopeStats`](#scopestats) | type | A point-in-time view of one scope |
| [`LingerOverride`](#lingeroverride-and-lingerwindow) | var | Test hook for the linger window |
| [`LingerWindow`](#lingeroverride-and-lingerwindow) | func | Reads that hook; deprecated, for files generated before v3.5.0 |
| [`Initializer`](#capability-interfaces) … [`Readier`](#capability-interfaces) | types | The seven capability interfaces |
| [`Main`](#main) | func | The whole of a service's `main`: build, run, stop on a signal, exit with a status |
| [`MainOption`](#mainoption-shutdowntimeout-and-logger) | type | `ShutdownTimeout`/`Logger`'s return type |
| [`ShutdownTimeout`](#mainoption-shutdowntimeout-and-logger), [`Logger`](#mainoption-shutdowntimeout-and-logger) | funcs | `Main`'s shutdown deadline and logger |
| [`ExitCode`](#exitcode) | func | The exit status a `Run` error and a `Shutdown` report add up to |
| [`Report`](#report) | type | Every node's outcome for one pass |
| [`NodeResult`](#noderesult) | type | One node's outcome |
| [`NodeStatus`](#nodestatus) | type | `ok`, `failed`, or `abandoned` |
| [`NodeError`](#nodeerror) | type | One node's `Init` or `Run` failure, naming the node |
| [`MergeNodeResults`](#mergenoderesults) | func | Combines a node's per-phase results; deprecated, for files generated before v3.5.0 |
| [`RunStop`](#runstop) | func | Runs a stop call under a budget; deprecated, for files generated before v3.5.0 |
| [`DefaultStopBudget`](#defaultstopbudget) | var | The budget every stop call gets |
| [`DefaultInitBudget`](#defaultinitbudget-and-defaultrungrace) | var | The budget each `Init` call gets; zero, meaning none, by default |
| [`DefaultRunGrace`](#defaultinitbudget-and-defaultrungrace) | var | How long `Run` waits for its runners once its context ends |
| [`Graph`](#graph-and-graphnode), [`GraphNode`](#graph-and-graphnode), [`GraphScope`](#graphscope) | types | The resolved graph as data |
| [`GraphHTTP`](#graphhttp), [`GraphRoute`](#graphhttp), [`GraphUse`](#graphhttp), [`GraphExtractor`](#graphhttp) | types | The emitted HTTP plan as data |
| [`StartupReport`](#startupreport-and-startupnode), [`StartupNode`](#startupreport-and-startupnode) | types | Per-node `Init` timings |
| [`Response`](#the-response-family) | type | The sealed base every response kind implements |
| [`Json`](#the-response-family), [`Xml`](#the-response-family) | types | Typed response wrappers — the signature carries the schema |
| [`JSON`](#the-response-family), [`XML`](#the-response-family) | funcs | Construct the typed wrappers |
| [`Text`](#the-response-family), [`HTML`](#the-response-family), [`Blob`](#the-response-family), [`Redirect`](#the-response-family), [`Stream`](#the-response-family) | funcs | The untyped response kinds |
| [`WriteResponse`](#the-response-family) | func | Writes any Response; called by generated adapters |
| [`HTTPStatus`](#httpstatus-and-status) | type | One HTTP status, usable as an error |
| [`Status`](#httpstatus-and-status) | var | The table of statuses (`Status.NOT_FOUND`, …) |
| [`StatusFor`](#httpstatus-and-status) | func | The status for any code, the table's entry when it has one |
| [`HTTPConfig`](#httpconfig) | type | The emitted servers' listen/TLS/limit config |
| [`HTTPListener`](#httpconfig) | type | One named group's listener inside `HTTPConfig.Groups` |

## Markers

All of them exist to be read, not run. Each one panics if executed, which is what a missing
`servoinject` build tag or a skipped generation looks like at run time — a loud failure instead of a
nil app. Full semantics are on [Spec file and markers](spec.md).

### `Build`

```go
func Build(...Marker)
```

Declares an injector's roots and bindings. Every argument must be a marker call written inline with
explicit type arguments.

### `Root`

```go
func Root[T any]() Marker
```

Declares `T` as a root: `T` and everything it transitively depends on is constructed, and nothing
else.

### `Bind`

```go
func Bind[I, C any]() Marker
```

Declares that concrete type `C` satisfies interface `I` wherever `I` is requested. Wins over
structural search, including over an otherwise-unambiguous match.

### `Override`

```go
func Override[I, C any]() Marker
```

Declares a test-only replacement for `I`, used only when emitting `NewTestApp`. Takes priority over
a `Bind` for the same interface.

### `Scoped`

```go
func Scoped[T, I any](...ScopeOption) Marker
```

Declares `T` as a keyed, refcounted, lifecycle-managed instance instead of a singleton, reachable
through the accessor interface `I` that you declare in your own package. `T` must have a `ScopeKey`
method. Panics if it ever runs.

Full treatment: [Scoped instances](scopes.md).

### `HTTP`

```go
func HTTP(...HTTPOption) Marker

type HTTPOption struct{}

func Group(name string) HTTPOption
func Use[T any](...HTTPOption) HTTPOption
func Route(pattern string) HTTPOption
func Extract[T any]() Marker
```

`HTTP` declares that this injector serves the module's `//servo:` route directives: `servo
generate` emits one server per served group — router, typed request decoding, middleware chain,
lifecycle — into the generated file. The graph must provide a [`*HTTPConfig`](#httpconfig).
`Group` declares a named listener group (and, inside `Use`, selects one); `Use[T]` attaches a
middleware node — one with a `Middleware(next http.Handler) http.Handler` method, or one that is
itself a `func(http.Handler) http.Handler` — server-, group- or route-wide;
`Route` selects one route inside a `Use`; `Extract[T]` declares an extractor node
(`Extract(r *http.Request) (V, error)`) that turns handler parameters of type `V` into per-request
values. All panic if they ever run.

Full treatment: [HTTP routes](http.md); the marker grammar is on
[Spec file and markers](spec.md).

### `Value`

```go
func Value[T any]() Marker
```

Declares that `T` is supplied by the caller rather than built by a provider. It beats any provider
that also produces `T` — declaring one is how you say "this comes from the caller", which is only
meaningful if it wins — and a declared `T` nothing in the graph depends on is a generate-time
diagnostic rather than a struct field every caller keeps supplying and the app never reads.

Declaring at least one changes the generated API additively: the injector keeps `New(ctx)` and gains
`type Values struct{…}` and `func NewWith(ctx context.Context, v Values) (*App, error)`. See
[Generated API](generated-api.md#values-and-newwith), and [Spec file and markers](spec.md#value) for the full contract.

### `Include`

```go
func Include(func() []Marker) Marker
```

Splices the marker list returned by the named function into this `Build` call, so a set of
declarations shared by several injectors is written once. The function is **named, never called**:
its body is read as syntax exactly as `Build`'s own argument list is, which is why it must be
exactly `return []servo.Marker{ …marker calls… }` and why the file it lives in needs the
`servoinject` build tag the same way a spec file does.

It may name a function in another package, includes may nest, a cycle is a diagnostic, and a
`Bind`/`Override` written locally in the spec file supersedes an included one for the same
interface. Full contract: [Spec file and markers](spec.md#include).

### `ConfigFile`

```go
func ConfigFile(path string) Marker
```

Declares the config file this injector's [`//servo:config`](config.md) types read alongside the
environment. The path must be a string literal ending in `.json`, `.yaml`, `.yml`, or `.toml` —
the extension decides which decoder the generated code carries. Precedence per setting is default,
then file, then environment; `CONFIG_FILE` overrides the path at runtime. At most one per `Build`,
and one no config in the graph would read is a diagnostic. Full contract:
[Generated configuration](config.md) and [Spec file and markers](spec.md#configfile).

### `Trace`

```go
func Trace[I any](...TraceOption) Marker
func Expected(err error) TraceOption
type TraceOption struct{}
```

Declares that every consumer of interface `I` receives a generated OpenTelemetry wrapper around
whatever `I` resolves to: a span named `<pkg>.<I>/<Method>` around every method with a leading
`context.Context`, the span's context handed on, a non-nil error recorded unless an `Expected`
option names it. The wrapper depends on `go.opentelemetry.io/otel/trace.TracerProvider` as an
ordinary graph node your own constructor supplies. `Expected` is only legal inside `Trace` and
names a package-level error variable or constant, read as syntax and never evaluated.
`TraceOption` carries no data. Full contract: [Traced interfaces](tracing.md).

### `Linger` and `Max`

```go
func Linger(time.Duration) ScopeOption
func Max(int) ScopeOption

const DefaultLinger = 30 * time.Second
const DefaultMax    = 10_000
```

`Linger` is how long a scope keeps an instance alive after its last holder releases it; `Max` caps
how many keys it will hold instances for at once. Both arguments must be constant expressions —
the spec file is read, never run — and both panic if they ever execute.

`DefaultLinger` and `DefaultMax` are what `servo generate` bakes in when the option is omitted.
They are generate-time constants: changing them changes what the *next* generation emits and has no
effect on already-generated code.

### `Marker`

```go
type Marker struct{}
```

The opaque return type of every `Build` marker — `Root`, `Bind`, `Override`, `Scoped`, `HTTP`,
`Extract`, `Value`, `Include`, `ConfigFile` and `Trace`. Carries no data; it exists so `Build`'s argument list type-checks, and so an included
function has a slice element type to return.

### `ScopeOption`

```go
type ScopeOption struct{}
```

`Linger` and `Max`'s opaque return type, for the same reason `Marker` exists: it gives them a type
that makes `Scoped`'s argument list type-check. It carries no data.

## Capability interfaces

```go
type Initializer interface{ Init(ctx context.Context) error }
type Runner      interface{ Run(ctx context.Context) error }
type Drainer     interface{ Drain(ctx context.Context) error }
type Flusher     interface{ Flush(ctx context.Context) error }
type Finalizer   interface{ Stop(ctx context.Context) error }
type Healther    interface{ Health(ctx context.Context) error }
type Readier     interface{ Ready(ctx context.Context) error }
```

Detected with `types.Implements` at generation time, never with a runtime type assertion. Declare
these method signatures on your component and it joins the corresponding phase; declare none and no
lifecycle code is emitted for it. There is no base type to embed and nothing to register.

What each one is for, and when to write one:

| Interface | It means | Write one when |
| --- | --- | --- |
| `Initializer` | Acquire what construction could not: dial the connection, open the file, run the migration. Failure aborts startup and rolls back everything already built. | Your constructor can produce the value but cannot yet *use* it. Keep the constructor pure and put the I/O here. |
| `Runner` | A loop that owns the process's time: an HTTP server, a queue consumer, a ticker. Returns when its context is cancelled. | Your component does work nobody calls it for. |
| `Drainer` | Stop accepting new work; let what is in flight finish. | Requests, messages or jobs can be mid-flight when shutdown starts, and cutting them off would lose them. |
| `Flusher` | Push buffered state somewhere durable. | You hold data in memory that would be lost on exit — a write buffer, a metrics batch, a spool. |
| `Finalizer` | Release the resource: close the pool, disconnect, unsubscribe. | You hold something the OS or a remote service is also tracking. |
| `Healther` | "This process is not broken." Restart me if it fails. | A dependency can fail in a way that a restart would fix. |
| `Readier` | "Send me traffic." Take me out of the load balancer if it fails. | You can be alive but temporarily unable to serve — warming a cache, waiting on a leader election. |

**`Drain`, `Flush` and `Stop` must tolerate a component that was constructed but never `Init`ed.**
This is the one contract here that is easy to miss and expensive to get wrong. Both rollback paths
inside `New` reach the stop methods of nodes whose `Init` never ran — see
[Lifecycle](lifecycle.md#stopping-what-was-never-initialised) — so a `Stop` that assumes `Init`
succeeded panics during the unwind of an unrelated failure:

```go
// Wrong: nil pool if Init never ran.
func (d *DB) Stop(ctx context.Context) error { return d.pool.Close() }

// Right.
func (d *DB) Stop(ctx context.Context) error {
	if d.pool == nil {
		return nil
	}
	return d.pool.Close()
}
```

Three distinctions decide most of the questions people have here:

- **`Drain` vs `Stop`.** `Drain` waits for work to finish; `Stop` releases the resource. A server
  implements both: `Drain` stops accepting connections and waits for open ones, then `Stop` closes
  the listener. If you only have one of the two behaviours, only write that one.
- **`Health` vs `Ready`.** Neither is called by servo — you call `app.Health(ctx)` or
  `app.Ready(ctx)` yourself, typically from a handler. The distinction is the Kubernetes one, and
  conflating them means a slow cache warm-up gets your pod killed rather than briefly removed from
  the load balancer.
- **A method vs a cleanup `func()`.** Prefer `Stop`: it takes a context and returns an error, and a
  cleanup func does neither. Reach for the closure only when teardown is not about the value —
  removing a temp directory the value never knew about, undoing a global. See
  [the cleanup func](lifecycle.md#why-this-exists-when-finalizer-does).

Every one of these is optional and independent. Implement three and exactly those three are called.
When each runs, in what order, and under what budget:
[Lifecycle](lifecycle.md#the-seven-capabilities).

## Main

### `Main`

```go
func Main[A interface {
	Run(context.Context) error
	Shutdown(context.Context) Report
}](newApp func(context.Context) (A, error), opts ...MainOption)
```

The whole of a service's `main`:

```go
func main() { servo.Main(New) }
```

It builds the App with `newApp`, runs it until the process is told to stop, shuts it down under a
deadline, logs every failure on the way, and exits with [`ExitCode`](#exitcode)'s status. A graph
that declares a [`servo.Value`](spec.md#value) passes a closure calling `NewWith`, and so does a
`main` with work of its own between `New` and `Run`:

```go
servo.Main(func(ctx context.Context) (*App, error) {
	return NewWith(ctx, Values{Target: migrator.Target(*target)})
})
```

`newApp` is normally the generated `New`; anything with the generated `Run` and `Shutdown` will do.
`Main` never returns.

| What happens | `Main` | Exit status |
| --- | --- | --- |
| `newApp` fails | logs `servo: startup failed` at ERROR | 1 |
| A signal interrupts `newApp` | nothing: `New` rolled back and said so | 0, or 1 if the rollback was not clean |
| `Run` returns a failure | logs `servo: run failed`, then shuts down | 1 |
| `Run` returns `nil`, or the signal's cancellation | shuts down | 0 |
| `Shutdown`'s report is not clean | logs `servo: shutdown was not clean` | 1 |
| A second signal | one line on stderr, then exits at once | 128 plus the signal's number |

**Signals.** SIGINT, SIGTERM and SIGHUP each ask for a graceful stop: they cancel the context
`newApp` and `Run` were given, with a cause that reads like `signal.NotifyContext`'s (`terminated
signal received`) and matches `context.Canceled` under `errors.Is`. SIGHUP is on the list because
Go's default for it ends the process on the spot, and it is what a closed terminal or a dropped ssh
session sends: a service started from one would die mid-request, its connections cut and nothing
flushed. SIGQUIT is left alone — its default dumps every goroutine's stack, which is what a stop
that seems stuck calls for. Main subscribes before calling `newApp`, so a signal during startup is
never met by Go's default instead.

**A second signal** ends the process at once, with one line on stderr and the status a shell
reports for a process a signal killed — 130 for SIGINT, 143 for SIGTERM, 129 for SIGHUP:

```
servo: terminated signal received during shutdown, exiting with status 143 without finishing it
```

Main watches for it from the first signal until `Shutdown` begins — while the runners wind down, or
while an interrupted startup rolls back — where a second signal used to be swallowed by the first
one's subscription. From then on the generated `Shutdown`'s own watcher answers SIGINT and SIGTERM,
with the same line, and Main unsubscribes from those two first, so exactly one of them answers. It
keeps SIGHUP, which that watcher does not cover.

**Logging.** Each failure is one ERROR record, the error under `err`, through the
[`Logger`](#mainoption-shutdowntimeout-and-logger) option's logger or, without one, through
`slog.Default()` as it is when the record is written — so a logger the App installs as the default
while `New` builds it is the one that says why the process stopped. A record that logger would
drop, because its handler is not enabled for ERROR or fails to write, goes to stderr instead, as
`servo: startup failed: <err>`. The `main` this replaces used `log.Fatal`, which an App's
`slog.SetDefault` routes through the installed handler at INFO: behind a WARN-level logger, a failed
start exited 1 with nothing in the log at all.

**The shutdown deadline.** `Shutdown` gets a context of its own whose deadline is
[`ShutdownTimeout`](#mainoption-shutdowntimeout-and-logger) after the first signal, or after `Run`
returned when it returned without one. It is not `Run`'s context: that one is already cancelled —
its cancellation is what started the stop — and a teardown handed it would report every node
abandoned before asking any of them to stop. It is not an unbounded `context.Background()` either:
a supervisor sends SIGKILL when its grace period runs out whether or not the unwind has finished.
Counting from the signal keeps `Run`'s own wind-down, up to
[`DefaultRunGrace`](#defaultinitbudget-and-defaultrungrace), inside the same budget. Sizing it
against an orchestrator's grace period:
[Lifecycle](lifecycle.md#sizing-the-grace-period).

### `MainOption`, `ShutdownTimeout` and `Logger`

```go
type MainOption struct{ /* unexported */ }

func ShutdownTimeout(d time.Duration) MainOption
func Logger(l *slog.Logger) MainOption
```

**`ShutdownTimeout`** sets how long `Main` gives a stop: from the first signal, or from `Run`
returning on its own, to the end of `Shutdown`. It is the deadline of the context `Shutdown`
receives, so every node still to stop when it passes is reported abandoned rather than waited for,
and the process exits 1. The default, 25 seconds, fits inside the 30 Kubernetes gives a pod by
default; whatever it is set to, keep it below the supervisor's grace period with a margin. Zero or
less keeps the default: `Main` never shuts down without a deadline.

**`Logger`** sets the logger `Main` reports failures through. Without it, or given nil, `Main` uses
`slog.Default()` as it is when each line is written.

### `ExitCode`

```go
func ExitCode(runErr error, r Report) int
```

The status a process should exit with once `Run` has returned and `Shutdown` has reported: **0**
when `Run` returned `nil` or nothing but the cancellation of its context, and every node stopped
cleanly; **1** otherwise — a runner failed, panicked or would not stop, or a node failed or was
abandoned during `Shutdown`. `Main` exits with it, and a [hand-written `main`](lifecycle.md#writing-your-own-main) ends with it:

```go
os.Exit(servo.ExitCode(err, report))
```

It exists because the obvious `main` logs a failure and falls off the end, and returning from `main`
exits 0: a supervisor told to restart on failure never restarts the process, and a batch job that
failed reports success. 1 is what `log.Fatal` exits with, so a failure in `New` and one in `Run`
agree.

Only a cancellation counts as clean: `context.Canceled`, an error wrapping it, or a join of nothing
else. A join with anything else is a failure — `errors.Is` would find the cancellation and miss the
failure beside it — and so is `context.DeadlineExceeded`, since a deadline is not a shutdown signal.
So is a [`*NodeError`](#nodeerror), whatever it wraps: the generated `Run` wraps only failures in
one, and hands back the cancellation a runner returns on an ordinary stop unchanged.

The flip side is worth knowing for batch work: a job stopped by SIGTERM part-way through exits 0
like any other stopped service, and a scheduler may count that as a completed run. A job that must
report an interrupted run as a failure checks for the signal itself, in its own `main`.

## Reports

### `Report`

```go
type Report struct {
	Nodes []NodeResult `json:"nodes"`
}

func (r Report) Clean() bool
func (r Report) Err() error
func (r Report) Error() string
func (r Report) Unwrap() []error
```

Every node's outcome for one `Shutdown`, `Health` or `Ready` pass, in the order the framework
processed them. `Shutdown`'s report is in the order nodes were actually stopped, which is what makes
it assertable rather than assumed.

**`Clean`** reports whether every node reached `StatusOK`. An empty report is clean — a graph with
nothing to stop stopped fine.

**`Err`** is the report as an `error` when it is not clean, and `nil` when it is — the form that
answers `if err != nil` honestly (see the trap below): `if err := app.Shutdown(sctx).Err(); err !=
nil { … }`.

**`Error`** renders every non-OK node as one message, `; `-separated:
`*api.Server: failed: connection reset; *postgres.DB: abandoned: context deadline exceeded`. For a
clean report it returns the empty string.

**`Unwrap`** returns one wrapped error per node that carried one, so `errors.Is` and `errors.As`
traverse into them normally.

`Report` satisfies `error` **by value**, so it composes with `errors.Join` and `%w`, and the error a
failed `New` returns carries the rollback's report when that had something to say — retrieved with
`errors.As(err, &report)`; see [Lifecycle](lifecycle.md#init). It also means one trap worth naming:

```go
// Always non-nil, even on a completely clean shutdown.
var err error = app.Shutdown(ctx)

// Correct.
if r := app.Shutdown(ctx); !r.Clean() {
	log.Print(r)
}

// Also correct.
if err := app.Shutdown(ctx).Err(); err != nil {
	log.Print(err)
}
```

A `Report` value in an `error` interface is never nil. Check `Clean()`, or use `Err()`.

**As JSON**, a report is `{"nodes": [...]}`, each node `{"name": …, "status": "failed", "err":
"…"}` with `err` omitted when there is none — so a report served from a health endpoint or written
to a structured log says which node, what happened and why. The default encoding wrote the status
as a bare number and every error as `{}`. Decoding reads the same shape back; an error comes back as
its message only, since its type did not survive the encoding.

### `NodeResult`

```go
type NodeResult struct {
	Name   string
	Status NodeStatus
	Err    error
}
```

One node's outcome. `Name` is the node's full type string — the same string used as its identity
everywhere else, so it lines up with `servo explain`, `servo graph`, and `GraphNode.Type`. `Err` is
nil when `Status` is `StatusOK`.

`MarshalJSON` writes `{"name": …, "status": …, "err": …}`, `err` being the error's message and
omitted when `Err` is nil; `UnmarshalJSON` reads it back, the error as an `errors.New` of its
message.

### `NodeStatus`

```go
type NodeStatus int

const (
	StatusOK NodeStatus = iota
	StatusFailed
	StatusAbandoned
)

func (s NodeStatus) String() string
func (s NodeStatus) MarshalText() ([]byte, error)
func (s *NodeStatus) UnmarshalText(text []byte) error
func (s *NodeStatus) UnmarshalJSON(data []byte) error
```

| Constant | `String()` | Meaning |
| --- | --- | --- |
| `StatusOK` | `ok` | Returned nil within its budget |
| `StatusFailed` | `failed` | Returned an error, or panicked, within its budget |
| `StatusAbandoned` | `abandoned` | Didn't return in time, and the goroutine was left running — or was never started, because its context was already done |

Any other value stringifies as `unknown`. `Health` and `Ready` never produce `StatusAbandoned` —
they apply no budget.

A status encodes as its `String()` form, in JSON and as text (a map key, a flag, a YAML scalar),
and decodes from the three names; anything else is refused. `UnmarshalJSON` also accepts the bare
number a status was encoded as before v3.5.0, so stored reports keep decoding — `encoding/json`
would otherwise refuse a number for a type with a text form.

### `NodeError`

```go
type NodeError struct {
	Node  string
	Phase string
	Err   error
}

func (e *NodeError) Error() string // "<node>: <phase>: <err>"
func (e *NodeError) Unwrap() error // Err
```

One node's failure in one lifecycle phase. The generated `New` returns one (or several, joined)
when an `Init` fails, and `Run` when a runner fails, panics, or is still running after
[`DefaultRunGrace`](#defaultinitbudget-and-defaultrungrace). `Node` is the node's type string —
the same identity `Report`, `GraphNode.Type` and `servo explain` use, or `"http"`/`"http:<group>"`
for an emitted server — and `Phase` is `"Init"` or `"Run"`. For a panic, `Err` carries the panic
value and the stack it was raised on.

It exists so a startup failure names its node: `dial tcp 127.0.0.1:5432: connect: connection
refused` does not say which of several components dialled. `errors.Is` and `errors.As` see through
it to `Err`, so code matching on a component's own errors keeps working; match the `*NodeError`
itself with `errors.As` when the node is what you need. A constructor's error is not wrapped — the
[construction phase](lifecycle.md#construct) returns it undiluted.

### `MergeNodeResults`

```go
func MergeNodeResults(name string, results ...NodeResult) NodeResult
```

Combines one node's per-phase results (`Drain`, `Flush`, `Stop`, cleanup) into the single per-node
outcome a `Report` enumerates. **Abandoned outranks failed outranks OK**, and every phase error is
joined with `errors.Join`.

**Deprecated:** generated code calls [`servoimpl`](#servoimpl)'s copy since v3.5.0. This one
remains, unchanged, for files generated earlier.

## Scopes

### Scope errors

```go
var ErrNoScopeKey  = errors.New("servo: no scope key in context")
var ErrNoLifetime  = errors.New("servo: context has no Done channel — …")
var ErrScopeFull   = errors.New("servo: scope is at its Max live-instance cap")
var ErrScopeClosed = errors.New("servo: scope is shut down")
```

| Error | Returned by | Means |
| --- | --- | --- |
| `ErrNoScopeKey` | Your own `ScopeKey` method | The context carries no key. A convention, not a requirement — return any error you like |
| `ErrNoLifetime` | `Acquire` | The context can never be done, so the release backstop would never fire. `Background`, `TODO` and `WithoutCancel` are refused |
| `ErrScopeFull` | `Acquire` | The scope already holds `Max` live instances and this key is not one of them |
| `ErrScopeClosed` | `Acquire` | `Shutdown` has begun; the scope no longer accepts acquires |

All four are distinct sentinels — match them with `errors.Is`.

### `ScopeStats`

```go
type ScopeStats struct {
	Live      int    `json:"live"`
	Refs      int    `json:"refs"`
	Acquires  uint64 `json:"acquires"`
	Evictions uint64 `json:"evictions"`
	Failures  uint64 `json:"failures"`
}
```

Returned by a generated accessor's `Stats()`. `Live` counts instances, including one whose teardown
is still running — so waiting for it to reach zero is a valid way to wait for a scope to go quiet.
`Refs` is outstanding references across every instance. `Acquires` and `Evictions` are monotonic
totals, and an eviction counts once its instance has finished draining and stopping. `Failures` is
how many of those evictions did not come out clean — the only signal there is for a mid-life
teardown that failed, since no `Report` is being assembled at the time.

`Live` and `Refs` are sampled a nanosecond apart under a scope other goroutines are still using:
a snapshot of a moving system, not two halves of one atomic read.

Test- and debug-facing. Wiring it to Prometheus is your job — servo exports no metrics.

### `LingerOverride` and `LingerWindow`

```go
var LingerOverride time.Duration = -1

func LingerWindow(declared time.Duration) time.Duration
```

Generated code reads the override exactly once per scope, inside `New`, to decide that scope's
window. `LingerOverride` replaces every declared window when it is non-negative — which is why the
"no override" sentinel is `-1` and not `0`: zero is a real policy.

**`LingerWindow` is deprecated:** generated code calls [`servoimpl`](#servoimpl)'s copy since
v3.5.0, which reads the same `LingerOverride`. This one remains for files generated earlier.

Set it through [`servotest.Linger`](servotest-package.md#linger) rather than directly. Like
`DefaultStopBudget`, it is a package variable, so tests that use it must not run in parallel with
each other or with tests that depend on a scope's real window, and must set it before `New`.
`servotest.Linger` enforces the first half: it calls `t.Setenv`, so Go refuses it in a parallel
test.

## HTTP

The types a `//servo:` handler and its emitted server exchange. Full semantics — the directive
grammar, binding rules, and the status contract — live on [HTTP routes](http.md); this is the API
surface only.

### The Response family

```go
type Response interface{ /* sealed */ }

type Json[T any] interface { Response; Value() T }
type Xml[T any]  interface { Response; Value() T }

func JSON[T any](v T) Json[T]                          // application/json
func XML[T any](v T) Xml[T]                            // application/xml
func Text(s string) Response                           // text/plain
func HTML(s string) Response                           // text/html
func Blob(contentType string, data []byte) Response    // caller-set type
func Redirect(url string) Response                     // Location header; pair with a 3xx Status
func Stream(contentType string, r io.Reader) Response  // io.Copy body

func WriteResponse(w http.ResponseWriter, code int, res Response) error
```

`Response` is what a handler's first result must satisfy. Its method is unexported, so every value
came from one of these constructors — which is why the family can grow without the generator
changing. The typed wrappers keep the payload schema in the signature; the untyped kinds declare
plain `servo.Response`. All are interfaces, so the error path is `return nil, err`, and a nil
response written through `WriteResponse` produces the status code alone (the 204 idiom:
`return nil, servo.Status.NO_CONTENT`). `WriteResponse` exists because generated adapters live in
the user's package, where the sealed method is unreachable. The typed constructors are spelled
`JSON`/`XML` because Go permits one identifier per name per package, and the types own
`Json`/`Xml`.

`JSON` and `XML` encode the whole payload before writing the status, and `Stream` owns its reader,
closing it (when it is an `io.Closer`) once the copy ends. What an error from `WriteResponse` means
depends on how far the write got. If it failed before anything was written — a payload that did
not encode, or a code outside 100–999 — `WriteResponse` has already answered with a `500` and the
standard `{"error": "Internal Server Error"}` body, and the error carries
`Status.INTERNAL_SERVER_ERROR`, so `errors.As` tells the caller the exchange is complete. Any other
error means the status line is out and the body is not whole — a failed write, or a `Stream`
whose reader failed partway — and the generated adapter breaks the connection so the client cannot
mistake the truncated body for a complete one.

### `HTTPStatus` and `Status`

```go
type HTTPStatus struct{ /* unexported */ }

func (s HTTPStatus) Code() int
func (s HTTPStatus) Error() string // canonical RFC 9110 text
func (s HTTPStatus) New(msg string) error
func (s HTTPStatus) Newf(format string, args ...any) error
func (s HTTPStatus) Wrap(err error) error
func (s HTTPStatus) Wrapf(format string, args ...any) error

var Status = struct{ CONTINUE, OK, CREATED, /* … the full RFC 9110 + IANA set */ HTTPStatus }{…}

func StatusFor(code int) HTTPStatus
```

`Status`'s fields are named as the registry spells them (`NOT_FOUND`, `UNPROCESSABLE_CONTENT`,
`CONTENT_TOO_LARGE`). It is a table to read, never to assign to: Go cannot make a struct variable
read-only, so assigning to a field compiles and changes what that name means to every handler in
the process. `StatusFor` covers the codes the table leaves out — nginx's 499, a status an upstream
answered — returning the table's own entry when there is one (`StatusFor(404) ==
Status.NOT_FOUND`) and a status carrying the bare code otherwise, whose text is `HTTP <code>`. The
zero `HTTPStatus`, like any code outside 100–999, is no status: a generated adapter answers `500`
for it.

An `HTTPStatus` is an error, and the constructors return errors carrying both the status and your
message, so `errors.As` recovers the code and `errors.Is` still finds a `%w`-wrapped cause — and
`errors.Is(err, Status.NOT_FOUND)` holds too. A bare 2xx entry returned in a handler's error
position is the deliberate way to pick a success code other than 200. `Wrap(nil)` returns the bare
status.

Under a 4xx status the response body is the error's whole `Error()`, every wrapped layer included,
so `Wrap` and `%w` belong only around errors written for the API's caller; answer anything else
with `New` or `Newf` in words of your own. Under a 5xx status the body is the canonical text alone
and the error is logged, so wrapping there is always safe. The full list of what a client can read
is on [HTTP routes](http.md#exactly-what-the-client-reads).

### `HTTPConfig`

```go
type HTTPConfig struct {
	IP   string // empty = all interfaces
	Port uint16

	CertFile string // both set → TLS
	KeyFile  string

	MaxBodyBytes   int64 // <= 0 = the generated 1 MiB default
	MaxHeaderBytes int   // 0 = http.DefaultMaxHeaderBytes

	ReadHeaderTimeout time.Duration // zero = none, matching net/http.Server
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	ShutdownDelay time.Duration // keep serving this long after readiness drops

	Groups map[string]HTTPListener // one listener per declared group
}

type HTTPListener struct {
	IP   string
	Port uint16
	CertFile, KeyFile string
	MaxBodyBytes int64
	MaxHeaderBytes int
	ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout time.Duration
	ShutdownDelay time.Duration
}
```

The emitted servers' configuration node: the flat fields are the default group's listener, and
each group declared with `servo.Group("name")` needs an entry in `Groups` — missing one fails
`New` with an error naming the group, and so does a TLS key pair that does not load. servo never
constructs one — you write an ordinary provider
(`func NewHTTPConfig() (*servo.HTTPConfig, error)`) and it resolves like any other dependency,
which is also why it is a struct and not options: where the values come from is your module's
business. A group's listener does not inherit the flat fields' values. Zero timeouts mean what
they mean on `net/http.Server`; the server warns when it starts with nothing bounding its header
read. `ShutdownDelay` is how long a server goes on answering after `Shutdown` drops its readiness,
while load balancers stop routing to it — see
[HTTP routes: Lifecycle](http.md#lifecycle).

## Stop budget

### `DefaultStopBudget`

```go
var DefaultStopBudget = 5 * time.Second
```

Bounds every shutdown and rollback phase call when no other budget is given. A package variable
rather than configuration, because parsing configuration is out of scope for a code generator — set
it before `New` if 5 seconds is wrong for your service.

Note that it applies **per phase call**, not per node: a component implementing `Drain`, `Flush` and
`Stop` can consume three budgets in the worst case.
[`servotest.Timeout`](servotest-package.md#timeout) shrinks it for a single test and restores it via
`t.Cleanup`.

### `DefaultInitBudget` and `DefaultRunGrace`

```go
var DefaultInitBudget time.Duration      // zero: no budget
var DefaultRunGrace = 5 * time.Second
```

Package variables like `DefaultStopBudget`, read by generated code at call time — set them before
`New` and `Run`, and keep tests that change them out of parallel with tests that build an app.

**`DefaultInitBudget`** bounds each `Init` call during `New`, and every scoped instance's `Init`.
Zero, the default, means no bound: `New` waits for every `Init` it started. Above zero it is the
deadline of the context each `Init` receives and the limit on waiting for it — an `Init` that
respects its context fails at the deadline with an error naming the budget, and one that ignores
it is abandoned at the same moment, left running, while `New` rolls back. See
[Lifecycle](lifecycle.md#init).

**`DefaultRunGrace`** is how long the generated `Run` keeps waiting for its runners once its
context is done — the shutdown signal, or another runner failing. A runner still running past it
is left running, and `Run` returns a [`*NodeError`](#nodeerror) naming it, so `main`'s `Shutdown`
can begin. Five seconds matches `DefaultStopBudget`; a deployment sizing its termination grace
period should count it alongside the shutdown deadline. See [Lifecycle](lifecycle.md#run).

### `RunStop`

```go
func RunStop(ctx context.Context, budget time.Duration, name string, fn func(context.Context) error) NodeResult
```

Runs `fn` in its own goroutine under a context derived from `ctx` with `budget` as its deadline, and
returns:

- `StatusOK` if `fn` returned nil in time
- `StatusFailed`, with the error, if it returned an error in time
- `StatusFailed`, with the panic value and the stack, if it panicked
- `StatusAbandoned`, with the context's error, if it didn't return
- `StatusAbandoned`, without calling `fn` at all, if `ctx` was already done — the error says the
  stop was not attempted and wraps the context's own. Starting it anyway started it in the
  background, abandoned the same instant, so once a shutdown deadline had passed every node still
  to come began stopping at once and out of order while `main` exited.

The result channel is buffered, so a goroutine that outlives its budget can still send without
leaking — but it does keep running. That is the honest trade: servo reports an abandoned node rather
than blocking shutdown forever on a component that won't stop.

**A panic in `fn` is recovered**, and reported as one failed node:

```
*api.Server: failed: servo: *api.Server panicked during stop: send on closed channel
goroutine 42 [running]:
…
```

It has to be. The goroutine is servo's, not the caller's, so no `recover` in your `main` can reach a
panic here: unrecovered, one panicking `Stop` kills the process mid-unwind, leaving every node
behind it running and no `Report` to say which. Turning it into a `StatusFailed` result lets the
rest of the teardown finish and names the culprit.

Generated code calls a stop helper once per phase per node. You'd call this one directly only when
writing your own teardown around a graph.

**Deprecated:** generated code calls [`servoimpl`](#servoimpl)'s copy since v3.5.0. This one
remains for files generated earlier, and carries the same not-attempted fix.

## `servoimpl`

```go
import "github.com/okian/servo/v3/servoimpl"
```

The half of the runtime that only generated code calls: the `Init` phase and `Run` supervisor, the
stop helpers, the scope linger hook, the second-signal exit. **It is not for direct use**, and it
is exempt from servo's compatibility promise — its signatures and behaviour change with the
generator that calls them, since a generated file is regenerated, never edited, and always calls
the `servoimpl` of the servo version that wrote it. Reading it to understand a generated file is
exactly what it is for.

It exists because generated code lives in your package, so whatever it calls must be exported, and
exporting it from `servo` froze it as public API — which is why `RunStop`, `MergeNodeResults` and
`LingerWindow` are still here, deprecated. `servoimpl` imports `servo`, never the reverse, and
nothing beyond the standard library.

The exception to the exemption is its `ContractN` constants. Every generated file names the
contract it was written against, so a file that needs a newer runtime than your `go.mod` pins
fails to compile on that line — next to a comment naming the servo version it needs — rather than
somewhere inside a helper whose signature moved. A constant is never removed within a major
version.

## Graph data

### `Graph` and `GraphNode`

```go
type Graph struct {
	Nodes  []GraphNode  `json:"nodes"`
	Scopes []GraphScope `json:"scopes,omitempty"`
	HTTP   *GraphHTTP   `json:"http,omitempty"`
}

type GraphNode struct {
	Type         string   `json:"type"`
	Level        int      `json:"level"`
	Deps         []string `json:"deps"`
	Capabilities []string `json:"capabilities"`
	Binding      string   `json:"binding"`
	Pos          string   `json:"pos"`
	Scope        string   `json:"scope,omitempty"`
	Wraps        string   `json:"wraps,omitempty"`
}
```

The resolved graph as plain data, returned by the generated `App.Graph()` and produced by
`servo graph --format=json` — the same struct populated by both paths, down to `nil` versus `[]` and
the form of every position, so the build-time and run-time views can't drift.

| Field | Contents |
| --- | --- |
| `Type` | Full type string, the node's identity |
| `Level` | `1 + max(level of deps)`; the unit of `Init` concurrency. `0` for a [supplied value](#value), which the app has before it builds anything |
| `Deps` | Direct dependencies, as type strings. `nil`, not `[]`, when there are none — the generated `App.Graph()` emits a Go literal, and `servo graph --format=json` matches it |
| `Capabilities` | Detected capability names, in a fixed order: `Initializer`, `Runner`, `Drainer`, `Flusher`, `Finalizer`, `Healther`, `Readier` |
| `Binding` | `explicit bind`, `sole candidate`, `sole implementation`, `supplied` for a `servo.Value`, or `traced` for a [`servo.Trace`](tracing.md) wrapper |
| `Pos` | The provider's declaration site — or, for a supplied value, the `servo.Value[…]()` call site, and for a trace wrapper the `servo.Trace[…]()` call site. Relative to the module root in both producers, so two checkouts at different absolute paths agree; a provider in another module is named by its package's import path plus the file name, and one in the standard library as `$GOROOT/src/…` |
| `Scope` | The key type of the scope this node belongs to, or empty for a singleton |
| `Wraps` | For a trace wrapper, the node it decorates — also its first dependency; empty for every other node |

Display-only: type strings are labels, never lookup keys, and there is no path from a `GraphNode`
back to the instance it describes.

### `GraphScope`

```go
type GraphScope struct {
	Key       string   `json:"key"`
	Linger    string   `json:"linger"`
	Max       int      `json:"max"`
	Accessors []string `json:"accessors"`
	Members   []string `json:"members"`
	Borrows   []string `json:"borrows"`
}
```

One per declared scope, in `Graph.Scopes`. `Members` is what one instance holds; `Borrows` is the
singletons it shares with the rest of the app. A scoped node's `GraphNode` carries the same `Key`
string in its `Scope` field, and its `Level` counts from the scope's own floor.

Both `GraphNode.Scope` and `Graph.Scopes` are `omitempty`, so a graph with nothing scoped
serialises exactly as it did before scopes existed.

### `GraphHTTP`

```go
type GraphHTTP struct {
	Groups     []string         `json:"groups,omitempty"`
	Routes     []GraphRoute     `json:"routes"`
	Uses       []GraphUse       `json:"uses,omitempty"`
	Extractors []GraphExtractor `json:"extractors,omitempty"`
}

type GraphRoute struct {
	Method  string   `json:"method"`
	Pattern string   `json:"pattern"`
	Group   string   `json:"group,omitempty"` // "" = the default group
	Handler string   `json:"handler"`
	Args    []string `json:"args,omitempty"` // extracted parameters marked "(extracted)"
	Pos     string   `json:"pos,omitempty"`
}

type GraphUse struct {
	Type  string `json:"type"`
	Scope string `json:"scope"` // "server", "group <name>", or "route <METHOD /pattern>"
}

type GraphExtractor struct {
	Type     string `json:"type"`
	Produces string `json:"produces"`
}
```

The emitted HTTP plan as data, in `Graph.HTTP`: the declared groups (the default group is
implicit), every served route with its handler and arguments, each `servo.Use` attachment with
what it wraps, and each extractor with the parameter type it produces. `Graph.HTTP` is omitted
entirely when the spec declares no `servo.HTTP()`, so a graph without it serialises exactly as
before.

### `StartupReport` and `StartupNode`

```go
type StartupReport struct {
	Nodes []StartupNode `json:"nodes"`
}

type StartupNode struct {
	Type     string        `json:"type"`
	Duration time.Duration `json:"duration"`
}
```

Per-node `Init` timing, recorded by the generated `New` and returned by `App.Report()`. Only nodes
implementing `Initializer` appear. Entries are ordered by level, and within a level that ran
concurrently, by completion.
