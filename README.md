![Go](https://github.com/okian/servo/workflows/Go/badge.svg)

# servo v2

Servo is a build-time code generator that resolves a Go application's object graph from
constructor signatures and emits plain Go source that constructs, starts, supervises, and shuts
down the application in dependency order.

No reflection. No runtime registry. No `init()`. No hand-written wiring.

The generated file is ordinary Go: compiler-checked, IDE-navigable, steppable in a debugger, and
readable by a human at 3am.

**v2 is a from-scratch rewrite of `servo` and shares no API with v1.** v1 was a runtime lifecycle
sequencer built on a global registry, a hand-maintained `order int`, and `Initialize(ctx) error`
with no parameters — components found each other through package-level globals. v2 replaces all
of that: dependencies are declared only by constructor parameters, and resolution happens at
build time, not runtime.

See [ARCHITECTURE.md](ARCHITECTURE.md) for how the load → scan → resolve → emit pipeline fits
together and why it's shaped the way it is.

## Quick start

```
go install github.com/okian/servo/v2/cmd/servo@latest
```

Write ordinary constructors — no import of `servo` required:

```go
// store/store.go
package store

type Store interface{ Get(key string) string }

// postgres/postgres.go
package postgres

func New(log *logger.Logger) (*DB, error)      { ... }
func (d *DB) Get(key string) string            { ... } // satisfies store.Store
func (d *DB) Init(ctx context.Context) error   { ... } // connect
func (d *DB) Stop(ctx context.Context) error   { ... } // disconnect
func (d *DB) Health(ctx context.Context) error { ... }

// api/api.go
package api

func New(s store.Store) *Server { ... } // depends on the interface, not postgres directly
```

Scaffold a spec file (`servo init`) declaring the roots — everything they transitively depend on
is what gets built; nothing else is:

```go
//go:build servoinject

package main

import (
	"example.com/app/api"
	"example.com/app/postgres"
	"example.com/app/store"
	"github.com/okian/servo/v2/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *postgres.DB](), // needed once a 2nd store.Store exists; harmless here
	)
}
```

Run `servo generate`. It emits `servo_gen.go` next to the spec file, containing `New`, `Run`,
`Shutdown`, `Health`, `Ready`, `Graph`, and `Report` — construction in dependency order, lifecycle
calls only for the capabilities a type actually implements, reverse-order shutdown with budgets
and a per-node report. `main.go` never grows with the graph — it stays this same shape:

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Run(ctx); err != nil {
		log.Print(err)
	}
	if r := app.Shutdown(context.Background()); !r.Clean() {
		log.Print(r)
	}
}
```

A complete, runnable version of the above — including a genuinely ambiguous interface binding, an
error-returning constructor with rollback, and every lifecycle capability — lives in
[`examples/basic`](./examples/basic).

## Interfaces vs. concrete types

A dependency's declared type decides how it resolves — there's no separate mode to opt into:

- **Parameter is an interface** (`func New(s store.Store) *Server`): Servo scans every candidate
  in scope for the one whose result type structurally implements it (`types.Implements`, checked
  at generation time, never at runtime). Exactly one implementation auto-binds. Zero is a
  missing-provider error; two or more is an ambiguity error, resolved by adding
  `servo.Bind[store.Store, *postgres.DB]()`.
- **Parameter is a concrete type** (`func New(l *logger.Logger) *DB`): Servo looks for the one
  function that returns exactly that type. No interface is involved and `Bind` doesn't apply —
  there's nothing to disambiguate unless a second function also returns that same concrete type
  (see [Multiple instances of the same type](#multiple-instances-of-the-same-type) below).

An explicit `Bind` always wins even over an otherwise-unambiguous interface match, so it also works
as a deliberate override, not just a tie-breaker.

Declare an interface where a component should accept any of several implementations; declare a
concrete type where there's exactly one. Servo follows whichever you wrote — it never converts one
into the other, and nothing about the *shape* of a dependency changes based on whether other parts
of the graph happen to use interfaces or concrete types elsewhere.

## Capabilities

A component implementing none of these is just constructed and held; no lifecycle code is emitted
for it. Components never import `servo` to implement them — detection is structural
(`types.Implements`), checked entirely at generation time:

```go
type Initializer interface{ Init(ctx context.Context) error }
type Runner      interface{ Run(ctx context.Context) error }
type Drainer     interface{ Drain(ctx context.Context) error }
type Flusher     interface{ Flush(ctx context.Context) error }
type Finalizer   interface{ Stop(ctx context.Context) error }
type Healther    interface{ Health(ctx context.Context) error }
type Readier     interface{ Ready(ctx context.Context) error }
```

`Init` runs in topological order, one node per level or an `errgroup` across a level with more
than one. `Run` launches every `Runner` into an `errgroup.WithContext`, so one failing runner
cancels the rest. `Shutdown` runs in reverse-topological order — Drain, then Flush, then Stop (plus
any cleanup func the constructor returned) — each phase budgeted, reported as stopped, failed, or
abandoned, never claiming a clean stop it didn't earn. A second interrupt/term signal during
shutdown forces an immediate exit.

## Multiple instances of the same type

This section is about a narrower problem than [interface vs. concrete-type
resolution](#interfaces-vs-concrete-types) above: two constructors returning the exact same
concrete type, with no interface involved at all. Identity in the graph is purely by type — there
is no tagging mechanism (the `Key` type carries a `Tag` field for it, but nothing in the public API
sets one yet). So two constructors returning the same type aren't two instances, they're an
ambiguity:

```
$ servo generate
servo: no provider for *sqsaccounts.Client
  2 functions produce *sqsaccounts.Client — remove or rename all but one:
      processor.NewClientA   processor/naive.go:5:6
      processor.NewClientB   processor/naive.go:6:6
```

The fix — needed for two SQS clients on two AWS accounts, a primary and a replica database, two
tenant databases, anything of this shape — is to make the two instances distinct *types*, not just
distinct values of one type. [`examples/basic/queue`](./examples/basic/queue) and
[`examples/basic/relay`](./examples/basic/relay) are a complete, generated, tested example:

```go
// queue/queue.go
type Client struct{ Account string } // shared underlying client

// Distinct types are what makes these two separate, unambiguous graph
// nodes — both wrap the same Client, each constructed against a
// different account's credentials.
type OrdersAccount struct{ *Client }
type AuditAccount struct{ *Client }

func NewOrdersAccount() *OrdersAccount { return &OrdersAccount{Client: &Client{Account: "111111111111"}} }
func NewAuditAccount() *AuditAccount   { return &AuditAccount{Client: &Client{Account: "222222222222"}} }
```

A component that needs both just takes both — embedding means every `*Client` method (`Send`, here)
is still directly available, no delegation boilerplate:

```go
// relay/relay.go — forwards an order event from the orders account's
// queue into the audit account's queue, a realistic reason one component
// needs two distinct accounts of the same underlying type at once.
func New(orders *queue.OrdersAccount, audit *queue.AuditAccount) *Relay {
	return &Relay{orders: orders, audit: audit}
}

func (r *Relay) Init(ctx context.Context) error {
	r.OrdersResult = r.orders.Send("order-created")
	r.AuditResult = r.audit.Send("order-created (audit copy)")
	log.Println(r.OrdersResult)
	log.Println(r.AuditResult)
	return nil
}
```

`servo explain relay.Relay` confirms exactly the shape you'd expect — two independent level-1
nodes, both feeding the one level-2 consumer that needs them:

```
$ servo explain relay.Relay
*example.com/servobasic/relay.Relay
  provider:     relay.New (relay/relay.go:24:6)
  binding:      sole candidate
  level:        2
  depends on:   *example.com/servobasic/queue.OrdersAccount, *example.com/servobasic/queue.AuditAccount
  depended on:  none
  capabilities: Initializer
```

Running the example logs both, distinctly, at startup (timestamps from Go's `log` package
trimmed below):

```
[account 111111111111] order-created
[account 222222222222] order-created (audit copy)
```

If nothing ever needs just one of the two on its own, a simpler alternative is a single component
holding both as plain fields (`type Clients struct{ Orders, Audit *queue.Client }`) instead of two
separate graph nodes — reach for distinct types when different consumers need different instances,
and a bundling struct when they're always needed together.

## Diagnostics

Missing providers, ambiguous bindings, and cycles are build failures with source positions —
never a runtime panic, never a guess:

```
$ servo generate
servo: no provider for example.com/servobasic/store.Store
  needed by *example.com/servobasic/api.Server  api/api.go:15:6
  root                                          cmd/basic/spec.go:17:3

  3 types implement example.com/servobasic/store.Store — add one of:
      servo.Bind[example.com/servobasic/store.Store, *example.com/servobasic/memory.Store]()      memory/memory.go:15:6
      servo.Bind[example.com/servobasic/store.Store, *example.com/servobasic/mockstore.Store]()      mockstore/mockstore.go:29:6
      servo.Bind[example.com/servobasic/store.Store, *example.com/servobasic/postgres.DB]()      postgres/postgres.go:13:6
```

(reproduced by deleting the `servo.Bind[store.Store, *postgres.DB]()` line from
[`examples/basic/cmd/basic/spec.go`](./examples/basic/cmd/basic/spec.go) and running `servo generate`
— `mockstore.Store` shows up too because it's a real second-and-third implementation living in the
same module, used by the [Mocking](#mocking) section below)

## CLI

| Command | Purpose |
| --- | --- |
| `servo generate [--dir]` | Resolve and emit `servo_gen.go` for **every** injector found under `--dir` (and `servo_gen_test.go` per injector that declares `servo.Override`). Default command. |
| `servo check [--dir]` | Verify every injector found under `--dir` matches a fresh generation; prints a diff and reports every stale one, not just the first. |
| `servo graph [--dir] [--format=text\|json\|dot\|mermaid]` | Export one injector's resolved graph. |
| `servo explain <type> [--dir]` | Which provider was selected and why, its dependencies, dependents, level, and capabilities. |
| `servo why <type> [--dir]` | Shortest path from a root to that node. |
| `servo list [--rejected] [--all] [--dir]` | The candidate index, or every excluded function and the rule that excluded it. Defaults to the main module; `--all` includes stdlib/third-party. |
| `servo init [--dir]` | Scaffold a spec file with the correct build tag and a `go:generate` directive. |
| `servo doctor [--dir]` | Diagnose setup problems (missing build tag, stale/absent generated file) before `go generate` ever runs. |
| `servo migrate [--dir]` | Read v1 `Register(X{}, N)` calls and emit a v2 skeleton plus a report flagging duplicate order values. |
| `servo new component <Name>` / `servo new adapter <pkg>` | Scaffold a component or third-party wrapper. Never imports `servo`. |
| `servo new mock-adapter <moq\|mockery\|gomock> <GeneratedTypeName>` | Scaffold the adapter file a generated mock needs to become a valid provider (see [Mocking](#mocking)). |

`--dir` (default `.`) is where the module scan starts. `generate` and `check` process **every**
injector they find within it — a monorepo with `cmd/api`, `cmd/worker`, `cmd/migrator` each wiring
their own graph gets all three generated/checked in one pass, the same way `wire ./...` does, and a
CI job doesn't need updating when a new service is added. Commands that answer a question about
*one* graph (`graph`, `explain`, `why`, `list`, `doctor`) instead ask you to disambiguate with
`--dir` when more than one injector is in scope — pointing it at a specific injector's own
directory (e.g. `--dir cmd/api`) scopes the scan to just that one, since a `package main` can never
import another `package main`, so sibling injectors are structurally unreachable from it.

Every command accepts `--json` for machine consumption. `cmd/servo-vet` is a standalone
`go/analysis` analyzer flagging marker calls in files missing the `servoinject` build tag, so a
misconfigured spec file is caught in the editor, not at runtime.

**CI and pre-commit**: [`.github/workflows/go.yml`](./.github/workflows/go.yml) is a reference
workflow — build, vet, test, then `servo check` against every injector, so a constructor signature
change without a matching re-run of `servo generate` fails CI instead of shipping a stale generated
file. [`githooks/pre-commit`](./githooks/pre-commit) runs the same check locally; enable it in a
given clone with `git config core.hooksPath githooks` (it is not on by default).

## Testing (`servotest`)

```go
func TestApp(t *testing.T) {
	defer servotest.NoLeaks(t)                // goleak, clean by construction
	servotest.Timeout(t, 50*time.Millisecond) // exercise the abandoned-node path without a slow suite

	ctx := context.Background()
	app, err := NewTestApp(ctx) // generated only when the spec declares servo.Override
	if err != nil {
		t.Fatal(err)
	}
	...
	rec := servotest.NewRecorder(app.Report(), app.Shutdown(ctx))
	servotest.AssertStopOrder(t, rec, "*api.Server", "*postgres.DB") // asserted, not assumed
}
```

## Mocking

Servo does not ship a mock generator — generated code is plain Go, and any type
satisfying an interface can be wired in via `servo.Override`. The one thing every mocking library
needs from you: **a discoverable, dependency-only constructor**. `servo generate` resolves the
graph by calling a provider function it found — `func F(deps...) T` — never by evaluating a
composite literal written in a test file. Hand-written fakes already look like this (see
[`examples/basic/mockstore`](./examples/basic/mockstore)). Generated mocks sometimes don't, because
their constructors are built to take a `*testing.T`/`*gomock.Controller` for automatic expectation
verification — and that's inherently a per-test-function value, not something the graph has. The
fix in every case below is a few lines in a **separate, hand-written file** (never edit a
`// Code generated ... DO NOT EDIT` file — regenerating the mock would erase it).

[`examples/mocking`](./examples/mocking) wires all three integrations below as real, permanent,
`go test`-checked apps (a shared `store.Store`/`api.Server` pair, one small binary per tool, each
with the actual tool run for real and the generated mock committed) — not just prose. `servo new
mock-adapter <moq|mockery|gomock> <GeneratedTypeName>` scaffolds the adapter file for any of the
three directly, instead of retyping the pattern below by hand.

### `moq`

Real, runnable version: [`examples/mocking/moq`](./examples/mocking/moq).

`moq` generates a plain struct with function fields and no constructor at all:

```go
type StoreMock struct {
	GetFunc func(key string) string
	// ...
}
```

Add one, in a separate file:

```go
// mockstore/adapter.go
func NewStoreMock() *StoreMock { return &StoreMock{} }
```

```go
servo.Override[store.Store, *mockstore.StoreMock](),
```

```go
app, _ := NewTestApp(ctx)
app.storeMock.GetFunc = func(key string) string { return "mocked:" + key }
got := app.server.Lookup("user:42")   // "mocked:user:42"
app.storeMock.GetCalls()              // [{Key: "user:42"}]
```

### `testify` + `mockery`

Real, runnable version: [`examples/mocking/mockery`](./examples/mocking/mockery).

Mockery's `Store` type is a plain struct (`mock.Mock` zero-values fine), but its generated
constructor requires a `*testing.T`-shaped value to auto-register `AssertExpectations` on cleanup:

```go
func NewStore(t interface {
	mock.TestingT
	Cleanup(func())
}) *Store { ... }
```

That constructor is still a valid provider shape servo could call — which means a second,
zero-arg constructor producing the same `*Store` type would be a genuine ambiguity (two functions
producing the identical type; servo correctly refuses to guess which one to use). So **wrap
instead of competing**:

```go
// mocks/adapter.go
type StoreMock struct{ *Store } // embeds *mocks.Store — a distinct result type, no collision

func NewStoreMock() *StoreMock { return &StoreMock{Store: &Store{}} }
```

```go
servo.Override[store.Store, *mocks.StoreMock](),
```

```go
app, _ := NewTestApp(ctx)
app.storeMock.On("Get", "user:42").Return("mocked-value")

got := app.server.Lookup("user:42") // "mocked-value"

app.storeMock.AssertExpectations(t) // call this yourself — nothing auto-registered it
```

This is the cleanest of the three integrations: testify passes `t` explicitly to
`AssertExpectations(t)` at the point you call it — where a real `t` is naturally in scope — rather
than baking a reporter in at construction time. A failed expectation reports through `t` normally:
no panic, no crash, just a clean, isolated test failure.

### `go.uber.org/mock` (gomock)

Real, runnable version: [`examples/mocking/gomock`](./examples/mocking/gomock).

Same wrapping requirement as mockery, for the same reason (`NewMockStore(ctrl)` is a valid,
colliding provider shape). The harder part is `*gomock.Controller` itself — it isn't a zero-value
struct, and its `TestReporter` (just `Errorf`/`Fatalf`, nothing `*testing.T`-specific) has to come
from somewhere: `servotest.PanicReporter` supplies one, satisfying gomock's `TestReporter`
structurally without adding a gomock dependency to `servotest` itself.

```go
// mockgenstore/adapter.go
type MockStoreForServo struct {
	*MockStore
	Finish func()
}

func NewMockStoreForServo() *MockStoreForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &MockStoreForServo{MockStore: NewMockStore(ctrl), Finish: ctrl.Finish}
}
```

```go
app, _ := NewTestApp(ctx)
defer app.mockStoreForServo.Finish() // call this yourself, right after construction
app.mockStoreForServo.EXPECT().Get("user:42").Return("mocked-value")

got := app.server.Lookup("user:42") // "mocked-value"
```

**Be honest about the tradeoff here**: `Finish` must be called directly by the test (a plain
`defer`), never routed through servo's `(T, func())` cleanup shape — that would run it inside
`servo.RunStop`'s own goroutine during `Shutdown`, and an unmet expectation there panics in a
goroutine nothing can recover, crashing the whole test binary with no indication of which test was
running. Called directly in the test's own goroutine instead, an unmet expectation still panics
(`PanicReporter` has no `t.Fatalf` to soften it into) — Go's test runner reports which test failed
before it re-panics, but the process still exits, unlike testify's clean, isolated failure. For
strict expectation-count verification where that matters, construct and drive the mock directly in
an isolated unit test (`api.New(mockedStore)`, with a real `gomock.NewController(t)`) instead of
through `Override` — the graph-injection path is the right fit for exercising the wiring itself,
not for gomock's stricter verification style.

## Layout

```
cmd/servo/         CLI: generate, check, graph, explain, why, list, init, doctor, migrate, new
cmd/servo-vet/     standalone go/analysis binary
internal/load/     go/packages → typed syntax, spec-file discovery
internal/graph/    Key, Provider, candidate index, capability detection
internal/resolve/  roots → closure → order, levels, diagnostics
internal/emit/     source emission, import manager, name allocator
internal/render/   text, JSON, DOT, Mermaid graph renderers
servo/             markers + ~200-line runtime
servotest/         NoLeaks, Recorder, AssertStopOrder, Timeout, PanicReporter
examples/basic/    a complete, runnable example (separate module)
examples/mocking/  moq/mockery/gomock integrations, one binary each (separate module)
```

Core (`internal/*`, `cmd/servo`) depends on nothing beyond `golang.org/x/tools`; `servotest` alone
depends on `go.uber.org/goleak`. Neither the runtime package nor any generated output imports
`reflect`, and the generated package compiles with the `servo` module deleted save for the
~200-line runtime it calls into — both enforced as conformance checks, not just claimed.
