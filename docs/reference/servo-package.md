# servo package

```go
import "github.com/okian/servo/v3/servo"
```

**Who this is for:** anyone reading a generated file's calls into the runtime, or handling a
`Report` in their own code.

This package is two unrelated halves. The **markers** (`Build`, `Root`, `Bind`, `Override`,
`Scoped`, `Value`, `Include`, and the two scope options) are read as syntax by `servo generate` and
panic if they ever execute. The **runtime** — around 430 lines — is what generated code actually
calls at run time. It imports nothing outside the standard library, and neither it nor any generated
output imports `reflect`.

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
| [`Value`](#value) | func | Declares a type the caller supplies (marker) |
| [`Include`](#include) | func | Splices another function's marker list in (marker) |
| [`Linger`](#linger-and-max), [`Max`](#linger-and-max) | funcs | Scope policy (markers) |
| [`Marker`](#marker) | type | The markers' opaque return type |
| [`ScopeOption`](#scopeoption) | type | `Linger`/`Max`'s opaque return type |
| [`DefaultLinger`](#linger-and-max), [`DefaultMax`](#linger-and-max) | consts | What an omitted scope option becomes |
| [`ErrNoScopeKey`](#scope-errors) … [`ErrScopeClosed`](#scope-errors) | vars | The four scope errors |
| [`ScopeStats`](#scopestats) | type | A point-in-time view of one scope |
| [`LingerOverride`](#lingeroverride-and-lingerwindow) | var | Test hook for the linger window |
| [`LingerWindow`](#lingeroverride-and-lingerwindow) | func | Reads that hook; called by generated code |
| [`Initializer`](#capability-interfaces) … [`Readier`](#capability-interfaces) | types | The seven capability interfaces |
| [`Report`](#report) | type | Every node's outcome for one pass |
| [`NodeResult`](#noderesult) | type | One node's outcome |
| [`NodeStatus`](#nodestatus) | type | `ok`, `failed`, or `abandoned` |
| [`MergeNodeResults`](#mergenoderesults) | func | Combines a node's per-phase results |
| [`RunStop`](#runstop) | func | Runs a stop call under a budget |
| [`DefaultStopBudget`](#defaultstopbudget) | var | The budget every stop call gets |
| [`Graph`](#graph-and-graphnode), [`GraphNode`](#graph-and-graphnode), [`GraphScope`](#graphscope) | types | The resolved graph as data |
| [`StartupReport`](#startupreport-and-startupnode), [`StartupNode`](#startupreport-and-startupnode) | types | Per-node `Init` timings |

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
[Generated API](generated-api.md#values-and-newwith), and [Spec file and
markers](spec.md#value) for the full contract.

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

The opaque return type of every `Build` marker — `Root`, `Bind`, `Override`, `Scoped`, `Value` and
`Include`. Carries no data; it exists so `Build`'s argument list type-checks, and so an included
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

## Reports

### `Report`

```go
type Report struct {
	Nodes []NodeResult
}

func (r Report) Clean() bool
func (r Report) Error() string
func (r Report) Unwrap() []error
```

Every node's outcome for one `Shutdown`, `Health` or `Ready` pass, in the order the framework
processed them. `Shutdown`'s report is in the order nodes were actually stopped, which is what makes
it assertable rather than assumed.

**`Clean`** reports whether every node reached `StatusOK`. An empty report is clean — a graph with
nothing to stop stopped fine.

**`Error`** renders every non-OK node as one message, `; `-separated:
`*api.Server: failed: connection reset; *postgres.DB: abandoned: context deadline exceeded`. For a
clean report it returns the empty string.

**`Unwrap`** returns one wrapped error per node that carried one, so `errors.Is` and `errors.As`
traverse into them normally.

`Report` satisfies `error` **by value**, which composes nicely with `errors.Join` — the generated
`New` uses exactly that to combine an `Init` failure with the shutdown it triggered, *when that
shutdown had something to say*. When the rollback comes out clean it returns the bare error instead,
because joining a clean report appends its empty `Error()` as a trailing newline. It also means one
trap worth naming:

```go
// Always non-nil, even on a completely clean shutdown.
var err error = app.Shutdown(ctx)

// Correct.
if r := app.Shutdown(ctx); !r.Clean() {
	log.Print(r)
}
```

A `Report` value in an `error` interface is never nil. Check `Clean()`.

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

### `NodeStatus`

```go
type NodeStatus int

const (
	StatusOK NodeStatus = iota
	StatusFailed
	StatusAbandoned
)

func (s NodeStatus) String() string
```

| Constant | `String()` | Meaning |
| --- | --- | --- |
| `StatusOK` | `ok` | Returned nil within its budget |
| `StatusFailed` | `failed` | Returned an error within its budget |
| `StatusAbandoned` | `abandoned` | Didn't return in time; the goroutine was left running |

Any other value stringifies as `unknown`. `Health` and `Ready` never produce `StatusAbandoned` —
they apply no budget.

### `MergeNodeResults`

```go
func MergeNodeResults(name string, results ...NodeResult) NodeResult
```

Combines one node's per-phase results (`Drain`, `Flush`, `Stop`, cleanup) into the single per-node
outcome a `Report` enumerates. **Abandoned outranks failed outranks OK**, and every phase error is
joined with `errors.Join`. Called by generated code; exported because generated code is in your
package, not servo's.

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

Generated code calls `LingerWindow` exactly once per scope, inside `New`, to decide that scope's
window. `LingerOverride` replaces every declared window when it is non-negative — which is why the
"no override" sentinel is `-1` and not `0`: zero is a real policy.

Set it through [`servotest.Linger`](servotest-package.md#linger) rather than directly. Like
`DefaultStopBudget`, it is a package variable, so tests that use it must not run in parallel with
each other or with tests that depend on a scope's real window, and must set it before `New`.

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

Generated code calls this once per phase per node. You'd call it directly only when writing your own
teardown around a graph.

## Graph data

### `Graph` and `GraphNode`

```go
type Graph struct {
	Nodes  []GraphNode  `json:"nodes"`
	Scopes []GraphScope `json:"scopes,omitempty"`
}

type GraphNode struct {
	Type         string   `json:"type"`
	Level        int      `json:"level"`
	Deps         []string `json:"deps"`
	Capabilities []string `json:"capabilities"`
	Binding      string   `json:"binding"`
	Pos          string   `json:"pos"`
	Scope        string   `json:"scope,omitempty"`
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
| `Binding` | `explicit bind`, `sole candidate`, `sole implementation`, or `supplied` for a `servo.Value` |
| `Pos` | The provider's declaration site — or, for a supplied value, the `servo.Value[…]()` call site. Relative to the module root in both producers, so two checkouts at different absolute paths agree |
| `Scope` | The key type of the scope this node belongs to, or empty for a singleton |

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
