# Scoped instances

**Who this is for:** anyone whose object graph has something that isn't one-per-process — a chat
room per room name, a workspace per tenant, a client pool per region — and anyone debugging a
scope's lifetime, its reference count, or one of the diagnostics it can produce.

Everything else servo builds is a singleton: constructed once in `New`, held for the life of the
process. A scope is the one exception. A scoped type declares how to extract a **key** from a
`context.Context`; everyone presenting the same key shares one instance; when the last holder lets
go and a linger window closes, the instance is drained, stopped and evicted.

All of the machinery — the map, the reference count, the timer, the teardown ordering — is
generated. You write no registry.

## The four pieces

### 1. A key type

```go
// chat/chat.go
type RoomKey string
```

It has to be a **defined type**, not a bare `string`. Scope identity is type identity: two scopes
both keyed on `string` would be indistinguishable to the generator, and one would silently
resolve to the other. It also has to be comparable, since it keys the instance map.

### 2. A `ScopeKey` method

```go
type ctxKey struct{}

func WithRoom(ctx context.Context, key RoomKey) context.Context {
    return context.WithValue(ctx, ctxKey{}, key)
}

func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) {
    k, ok := ctx.Value(ctxKey{}).(RoomKey)
    if !ok || k == "" {
        return "", servo.ErrNoScopeKey
    }
    return k, nil
}
```

This is the only method servo finds **by name** rather than by `types.Implements`: the dependency
list varies per type, so there is no single interface shape to match against.

| Rule | Why |
| --- | --- |
| The method is named exactly `ScopeKey` | Detected by name; see above |
| The receiver is unnamed | Generated code calls it on a typed nil — the key has to be known *before* an instance can be chosen, so there is no instance to call it on. A blank `_` is accepted too, but staticcheck's ST1006 flags it and asks for the unnamed form |
| The first parameter is `context.Context` | The key comes from the request context |
| The results are exactly `(K, error)` | Without the error, a missing key becomes the zero `K` and every keyless caller silently shares one instance — a cross-tenant leak with no symptom |
| `K` is a defined, comparable, non-interface type | Scope identity is type identity |
| Remaining parameters are dependencies | Resolved as ordinary graph edges. They must be singletons — or another scope's accessor, which is an App field rather than an instance. Never this scope's own accessor: `Acquire` calls this method, so acquiring from inside it recurses without bound |
| The method is declared on the type, not promoted | A `ScopeKey` reached through an embedded field would dereference that field on a typed nil |

The unreachable receiver is checked, not assumed. `servo generate` refuses a named receiver, and
[`servo-vet`](cli.md#servo-vet) reports the same thing in your editor.

`servo.ErrNoScopeKey` is a convention, not a requirement — return any error you like. It is the one
place a component may import `servo`, and even there it is optional.

### 3. A `servo.Scoped` declaration

```go
servo.Build(
    servo.Root[*api.Server](),
    servo.Scoped[*chat.Room, chat.Rooms](
        servo.Linger(30*time.Second),
        servo.Max(10_000),
    ),
)
```

Two type parameters, reading like `Bind[I, C]`:

- `*chat.Room` — the scoped type, the one with the `ScopeKey` method.
- `chat.Rooms` — an interface **you declare in your own package**, which the generated accessor
  satisfies. servo cannot emit a type into your package, so without this there is nothing for a
  consumer's constructor to depend on.

Both options are optional. Omitted, they take `servo.DefaultLinger` (30s) and `servo.DefaultMax`
(10,000). See [The linger window](#the-linger-window) and [The Max cap](#the-max-cap) for what
they actually buy you.

### 4. An accessor interface

```go
// declared by you, in package chat
type Rooms interface {
    Acquire(ctx context.Context) (*Room, func(), error)
}
```

The generated accessor has exactly two methods. Your interface may declare either, or both, and
nothing else:

```go
Acquire(ctx context.Context) (*Room, func(), error)
Stats() servo.ScopeStats
```

A third method is a generate-time diagnostic, not a compile error inside a file you were told not
to read.

## Consuming a scope

An accessor is an ordinary dependency edge:

```go
func NewServer(rooms chat.Rooms, log *logger.Logger) *Server
```

and an ordinary acquire at the point of use:

```go
func (s *Server) Post(ctx context.Context, msg string) error {
    room, release, err := s.rooms.Acquire(ctx)
    if err != nil {
        return err
    }
    defer release()

    room.Post(msg)
    return nil
}
```

Putting the key in the context is your job, in your own middleware, at the transport edge:

```go
ctx = chat.WithRoom(r.Context(), chat.RoomKey(r.PathValue("room")))
```

servo ships no `net/http` or gRPC adapter. The moment it does, it stops being a codegen tool.

## What belongs to a scope

A node is **scoped** if it declares `ScopeKey`, or if it transitively depends on the scope's key
type or on another scoped node. Everything else stays a singleton, even when a scope is the only
thing that reaches it.

```
              ┌──────────────┐
              │ *api.Server  │  singleton — depends on chat.Rooms
              └──────┬───────┘
                     │ Acquire(ctx)
        ╔════════════▼════════════════════════════════╗
        ║ scope chat.RoomKey      one of each per key ║
        ║                                             ║
        ║   chat.RoomKey ─────► *chat.RoomLog         ║
        ║        │                     │              ║
        ║        └──────► *chat.Room ◄─┘              ║
        ╚═══════════════════════╤═════════════════════╝
                                │
                      ┌─────────▼────────┐
                      │ *logger.Logger   │  singleton — borrowed, not owned
                      └──────────────────┘
```

`*chat.RoomLog` never mentions a scope. It takes a `RoomKey`, and that alone puts it in the same
entry as the room, torn down alongside it. `*logger.Logger` is reached through the scope too, but
does not vary with the key, so it stays one instance shared by every room.

`servo graph`, `servo explain` and the generated file's header comment all report this split, so
which side a node landed on is never a guess:

```
$ servo explain chat.RoomLog
*example.com/servoscoped/chat.RoomLog
  provider:     chat.NewRoomLog (chat/chat.go:174:6)
  binding:      sole candidate
  lifetime:     scoped — one per example.com/servoscoped/chat.RoomKey, linger 30s, max 10000
  level:        1
  depends on:   example.com/servoscoped/chat.RoomKey, *example.com/servoscoped/logger.Logger
  depended on:  *example.com/servoscoped/chat.Room
  capabilities: Initializer, Flusher
```

**Two `servo.Scoped` declarations whose key types match are one scope.** One map, one reference
count, one linger timer per key, and every member of both sub-graphs in the same entry. That is
what makes a room and a room-scoped log share a lifetime instead of accidentally getting two.

## Lifetimes

The reference unit is **the caller's use of the instance** — not the resolution, and not the
context.

| Rule | The failure it prevents |
| --- | --- |
| The count drops on `release()`, not on `ctx.Done()` | Cancellation is not completion. A client disconnecting mid-handler cancels the context while `defer`s are still touching the instance |
| `context.AfterFunc` is a `sync.Once`-guarded backstop | A caller who forgets `release()` still releases when their request ends — later than ideal, but not never |
| A context with no `Done` channel is refused with `ErrNoLifetime` | `Background`, `TODO` and `WithoutCancel` never fire, so the backstop never runs and the count never reaches zero |
| Reaching zero and removing the key are one step | See [the eviction race](#the-eviction-race) |
| Teardown runs on a fresh `context.Background()` | Same reason `main` hands `Shutdown` a context of its own: the drain has to survive the cancellation that triggered it. No deadline, because a linger timer can fire it and there is no caller to take one from — `servo.RunStop` bounds each call at `servo.DefaultStopBudget` regardless |
| An instance's context is `WithoutCancel` of `New`'s | An instance outlives the request that created it. Hanging its `Run` loop off an acquirer's context would kill it when that one caller disconnects, while the instance is still live and referenced. Values propagate; cancellation does not |

### The linger window

A short `POST` holds a reference for milliseconds. With no linger, the count goes 0→1→0 per
request and the instance is rebuilt every time, losing whatever in-memory state made it worth
sharing. A reconnect after a network blip has exactly the same shape.

`servo.Linger(0)` is therefore a policy, not a default to fall into: it means *die with the last
holder*. It is the right choice when the key is unique per request and there is nothing to keep.

`servotest.Linger(t, d)` shrinks every scope's window for one test, so the eviction boundary can be
driven deliberately instead of waited out.

### The `Max` cap

Scope keys usually come from user input. Uncapped, an unauthenticated client allocates instances
until the process runs out of memory. Acquiring a key beyond the cap returns `servo.ErrScopeFull`
rather than allocating.

The cap counts **live instances**: keys inside their linger window with no holders left, and
instances that have left the map but are still draining and stopping. Counting only what is mapped
would make `Max` a bound on map size rather than on memory, which is not the thing it exists to
bound.

One consequence worth knowing: under `Max` pressure with a slow `Drain`, re-acquiring a key that is
merely finishing its teardown can return `ErrScopeFull` for the length of that teardown. The
instance still exists and still holds its memory, so admitting past it would defeat the cap.

## Per-instance lifecycle

Every capability is wired per instance, through the same `servo.RunStop` / `MergeNodeResults`
machinery as an app node.

| Phase | When | Order |
| --- | --- | --- |
| Construct | First `Acquire` of a cold key, in the acquiring goroutine | Dependency order; a failure rolls the entry back and leaves nothing in the map |
| `Init` | Immediately after construction | Level by level within the scope, concurrent within a level |
| `Run` | One goroutine per running instance, on the entry's own context | Started once construction and `Init` have both succeeded |
| `Drain` | At eviction | Every member, reverse construction order |
| *(cancel + wait)* | After every `Drain` | The entry's context is cancelled and every `Run` goroutine joined |
| `Flush`, `Stop`, cleanup | After the `Run` goroutines have returned | Per member, reverse construction order — each member's `Flush`, `Stop` and cleanup run together before the next member's |

An app node's `Drain`, `Flush` and `Stop` all run together, as one bundle per node. A scope splits
that in two, and the split is deliberate:

- **`Drain` is hoisted into its own pass across the whole entry**, before anything is cancelled, so
  a streaming consumer unblocks before its context is pulled out from under it.
- **`Flush` runs after the `Run` goroutines have returned**, so a buffer filled by `Run` is flushed
  rather than discarded.

Everything else matches the app-level contract exactly.

**`Health` and `Ready` are not wired for scoped nodes.** A report with one entry per live chat room
is not a report. Use `Stats()` instead.

A `Run` that returns because the entry's context was cancelled is a normal teardown, not a failure,
and is not reported as one. A `Run` that returns any other error has that error attached to the
instance's stop result — which means it surfaces at eviction, not when it happened. A component
that needs to react to its own `Run` failing sooner has to do that itself.

### Shutdown

`App.Shutdown` gains one step per scope, sequenced into the existing reverse-dependency teardown:
after every singleton that could still call `Acquire`, and before every singleton the instances
depend on. It reports **one `NodeResult` per scope**, merged from every entry it tore down — not
one per instance.

Ordering does most of the work by itself. Draining the server first ends the streams, which cancels
their contexts, which fires the release backstops, which drops the reference counts — so by the
time a scope is reached there is usually very little left in it.

Whatever is left, the scope waits for. An instance with holders still outstanding is not torn down
until they release, bounded by one stop budget — because a reference that was counted is a promise
that the instance stays usable until it is given back, and a caller who acquired successfully a
moment before someone else called `Shutdown` did nothing wrong. Past that bound the instance is
torn down under its holders anyway and the entry is reported abandoned in the scope's
`NodeResult`, exactly as an overrunning app node is: `Shutdown` has to terminate.

Once a scope is closed, `Acquire` returns `servo.ErrScopeClosed`, including for an acquirer that
was already waiting to join a live instance when `Shutdown` began.

## `Stats()`

```go
type ScopeStats struct {
    Live      int
    Refs      int
    Acquires  uint64
    Evictions uint64
    Failures  uint64
}
```

`Live` counts instances, including one whose teardown is still running — so waiting for `Live` to
reach zero is a valid way to wait for a scope to go quiet. `Acquires` and `Evictions` are monotonic
totals, and an eviction is counted once its instance has finished draining and stopping.

`Failures` is how many of those evictions did not come out clean. It exists because an instance
evicted mid-life has no `Report` to appear in — `Shutdown` is not running, and nobody is waiting on
that teardown — so a component that consistently fails to stop would otherwise leave no trace
anywhere. Watch it the way you would watch any error rate; which phase failed is not recovered.

This is test- and debug-facing. Wiring it to Prometheus is your job; servo exports no metrics.

## How the generated code works

One registry per scope; one entry per live key; one goroutine per entry owning that entry's
reference count and linger timer as loop-local variables. Because a dying entry is never revived —
a new incarnation is a new entry with a new goroutine — there is no ABA problem and no generation
counter.

```go
func (e *roomKeyEntry) loop() {
    refs := 1 // whoever built this entry holds the first reference
    var timer *time.Timer
    var linger <-chan time.Time
    // ...
    for {
        select {
        case <-e.joins:
            refs++
            stopTimer()
        case <-e.leaves:
            refs--
            if refs == 0 {
                timer = time.NewTimer(e.scope.linger) // stopTimer() elided
                linger = timer.C
            }
        case <-linger:
            e.evict() // ...
            return
        case <-e.scope.quit:
            e.drainRefs(refs) // wait for outstanding holders, bounded
            e.evict()         // ...
            return
        }
    }
}
```

The count starts at one rather than zero: whoever built the entry holds the first reference by
construction and never sends a join for it. Starting at zero would leave an entry whose creator
gave up before joining alive with no reference and no timer armed — live forever, held by nobody.

**`drainRefs` is what makes a successful `Acquire` mean something under `Shutdown`.** Evicting the
moment `quit` closes, whatever the count, leaves a window between any check an acquirer makes and
its own `return` statement — so an acquirer could be handed an instance that was drained and
stopped a microsecond later, with a nil error and no way to tell. No amount of re-checking closes
that: the check is a sample, and the loop is free to evict immediately after it. Waiting for the
count to reach zero removes the window instead of narrowing it, for both the creator and the
joiner, because both hold a reference the loop has already counted by the time they return.

The wait is bounded by one stop budget, so this is a guarantee with a stated limit rather than an
absolute one: a holder that never releases has its instance torn down under it after that budget,
and the entry comes back abandoned. Within the budget the race is gone; past it, it is reported.

**Map access** takes the read lock on the hit path, and the write lock with a second look on the
miss path: `sync.RWMutex` has no atomic upgrade, and two cold acquires of the same key would
otherwise both create, orphaning one entry and the goroutine it was about to start.

### The eviction race

Between an acquirer finding an entry and joining it, that entry can decide to die and stop reading.
The join is therefore abortable and the acquirer retries:

```go
select {
case e.joins <- struct{}{}:
    return e.room, e.releaser(ctx), nil
case <-e.dead:
    continue // lost the race; the retry misses and creates fresh
case <-s.quit:
    return nil, nil, servo.ErrScopeClosed // Shutdown began; do not wait out its drain
case <-ctx.Done():
    return nil, nil, ctx.Err()
}
```

Removing the key from the map **before** closing `dead` is what makes that retry terminate.
Reversed, it finds the same corpse forever.

### Cost

One goroutine per live entry. For the shared long-lived case this is negligible — ten thousand
rooms is roughly twenty megabytes of stacks, and each room already has member goroutines of its
own. For a `Linger(0)` request scope it is a goroutine per request to own a counter that goes 1→0
immediately; branching the emit path on `Linger == 0` would avoid it, at the cost of two scope
implementations to keep correct, and that trade is not worth making until the request-scoped case
has a real user.

## Diagnostics

Every one of these is a `servo generate` failure with source positions, and all four carry the full
needed-by chain from the root down to the node at fault. None is a runtime surprise.

| Diagnostic | Condition | Fix |
| --- | --- | --- |
| [Widening](diagnostics.md#widening) | A singleton depends on a scoped node | Depend on the accessor interface and `Acquire` per request, or make the consumer scoped too |
| [Cross-scope](diagnostics.md#cross-scope) | A node is reachable from two different key types | Depend on the inner scope's accessor and acquire inside the method that needs it |
| [Extractor cycle](diagnostics.md#extractor-cycle) | A `ScopeKey` parameter is itself scoped | The extractor runs before any instance exists; its dependencies must be singletons |
| [Undeclared scope](diagnostics.md#undeclared-scope) | A type has `ScopeKey` but no `servo.Scoped`, or the reverse | Add the missing half, or delete the `ScopeKey` method |

Widening is the feature's reason to exist. A singleton holding a scoped instance pins one key's
instance for the life of the process — the first room anyone joins becomes everyone's room — and
nothing about the running program says so. A hand-written registry beside servo gets no such check.

Those four are the named ones. Thirty-seven narrower messages — a stray scope key, a node two
scopes both claim, a scoped type declared as a `servo.Root`, an accessor someone tried to `Bind`,
every malformed `ScopeKey` signature and every rejected marker argument — are tabulated in
[Other scope errors](diagnostics.md#other-scope-errors).

## What scopes do not do

- **Cross-process scoping.** Two pods means two instances per key, unless your routing is sticky.
  Documented, not solved.
- **Nested scopes.** A room-scoped node depending on a tenant-scoped node is rejected. One instance
  per key *pair* means two reference counts and two linger windows with no single owner, and no
  obvious answer for what happens when the outer one evicts while the inner one is still held.
- **Persistence.** Instances are in memory and die with the process.
- **Transport integration.** You put the key in the context.

See [Limitations](../limitations.md#scopes-are-in-process-only) for the full list.

## The escape hatch

A scope is not the only way to get a keyed instance. A provider returning a defined func type works
today with no library support at all:

```go
type Acquire func(ctx context.Context) (*Room, func(), error)

func NewAcquire(log *logger.Logger) Acquire { ... }
```

`servo` accepts it, because a defined func type is a valid result type. What you lose is everything
this page is about: the node in the graph is the *func type*, and a func type implements nothing —
so `*Room`'s `Stop` is never called, and the resolver cannot see that anything behind that edge is
keyed, which loses the widening check entirely.

It is a legitimate escape hatch when you want the keying and none of the lifecycle. It is not a
smaller version of a scope.

## Worked example

[`examples/scoped`](https://github.com/okian/servo/tree/master/examples/scoped) is a complete,
runnable module: a chat room per room name, a room-scoped log that is only scoped transitively, a
shared logger, an API server that consumes the accessor, and the full race suite that gates this
feature in CI.
