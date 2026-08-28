# servo package

```go
import "github.com/okian/servo/v3/servo"
```

**Who this is for:** anyone reading a generated file's calls into the runtime, or handling a
`Report` in their own code.

This package is two unrelated halves. The **markers** (`Build`, `Root`, `Bind`, `Override`) are read
as syntax by `servo generate` and panic if they ever execute. The **runtime** — around 200 lines —
is what generated code actually calls at run time. It imports nothing outside the standard library,
and neither it nor any generated output imports `reflect`.

Your components never import this package. Capability interfaces are satisfied structurally, by
having the method.

## Index

| Identifier | Kind | |
| --- | --- | --- |
| [`Build`](#build) | func | Declares an injector (marker) |
| [`Root`](#root) | func | Declares a graph root (marker) |
| [`Bind`](#bind) | func | Binds an interface to a concrete type (marker) |
| [`Override`](#override) | func | Test-only binding (marker) |
| [`Marker`](#marker) | type | The markers' opaque return type |
| [`Initializer`](#capability-interfaces) … [`Readier`](#capability-interfaces) | types | The seven capability interfaces |
| [`Report`](#report) | type | Every node's outcome for one pass |
| [`NodeResult`](#noderesult) | type | One node's outcome |
| [`NodeStatus`](#nodestatus) | type | `ok`, `failed`, or `abandoned` |
| [`MergeNodeResults`](#mergenoderesults) | func | Combines a node's per-phase results |
| [`RunStop`](#runstop) | func | Runs a stop call under a budget |
| [`DefaultStopBudget`](#defaultstopbudget) | var | The budget every stop call gets |
| [`Graph`](#graph-and-graphnode), [`GraphNode`](#graph-and-graphnode) | types | The resolved graph as data |
| [`StartupReport`](#startupreport-and-startupnode), [`StartupNode`](#startupreport-and-startupnode) | types | Per-node `Init` timings |

## Markers

All four exist to be read, not run. Each one panics if executed, which is what a missing
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

### `Marker`

```go
type Marker struct{}
```

The opaque return type of `Root`, `Bind` and `Override`. Carries no data; it exists so `Build`'s
argument list type-checks.

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
lifecycle code is emitted for it. When each is called, in what order, and under what budget:
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
`New` uses exactly that to combine an `Init` failure with the shutdown it triggered. It also means
one trap worth naming:

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
- `StatusAbandoned`, with the context's error, if it didn't return

The result channel is buffered, so a goroutine that outlives its budget can still send without
leaking — but it does keep running. That is the honest trade: servo reports an abandoned node rather
than blocking shutdown forever on a component that won't stop.

Generated code calls this once per phase per node. You'd call it directly only when writing your own
teardown around a graph.

## Graph data

### `Graph` and `GraphNode`

```go
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
}

type GraphNode struct {
	Type         string   `json:"type"`
	Level        int      `json:"level"`
	Deps         []string `json:"deps"`
	Capabilities []string `json:"capabilities"`
	Binding      string   `json:"binding"`
	Pos          string   `json:"pos"`
}
```

The resolved graph as plain data, returned by the generated `App.Graph()` and produced by
`servo graph --format=json` — the same struct populated by both paths, so the build-time and run-time
views can't drift.

| Field | Contents |
| --- | --- |
| `Type` | Full type string, the node's identity |
| `Level` | `1 + max(level of deps)`; the unit of `Init` concurrency |
| `Deps` | Direct dependencies, as type strings |
| `Capabilities` | Detected capability names, in a fixed order: `Initializer`, `Runner`, `Drainer`, `Flusher`, `Finalizer`, `Healther`, `Readier` |
| `Binding` | `explicit bind`, `sole candidate`, or `sole implementation` |
| `Pos` | The provider's declaration site. Module-relative in generated code, absolute in CLI output |

Display-only: type strings are labels, never lookup keys, and there is no path from a `GraphNode`
back to the instance it describes.

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
