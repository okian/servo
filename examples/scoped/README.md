# Scoped instances

A chat server where each room is a keyed instance: everyone who presents the same room name shares
one `*chat.Room`, and it is drained, stopped and evicted once the last holder lets go and the
linger window closes.

This module is a separate Go module that `replace`s `github.com/okian/servo/v3` with this checkout,
like `examples/basic` and `examples/mocking`. Its `servo_gen.go` is committed and CI runs
`servo check --dir examples/scoped` against it, so drift between a constructor signature and a
re-run of `servo generate` fails the build rather than shipping stale generated code.

## The four pieces

**The spec** (`cmd/chat/spec.go`) declares the scope beside the root:

```go
servo.Build(
    servo.Root[*api.Server](),
    servo.Scoped[*chat.Room, chat.Rooms](
        servo.Linger(30*time.Second),
        servo.Max(10_000),
    ),
)
```

**The key extractor** (`chat/chat.go`) is what makes `*chat.Room` keyed rather than a singleton.
The receiver is unnamed on purpose — generated code calls this on a typed nil, because the key has
to be known before an instance can be chosen:

```go
func (*Room) ScopeKey(ctx context.Context) (RoomKey, error)
```

**The accessor interface** (`chat/chat.go`) is declared in the user's own package, because
`servo generate` cannot emit a type into it. The generated accessor satisfies it:

```go
type Rooms interface {
    Acquire(ctx context.Context) (*Room, func(), error)
    Stats() servo.ScopeStats
}
```

**The consumer** (`api/api.go`) depends on `chat.Rooms`, never on `*chat.Room`. A singleton holding
the scoped type directly is the widening diagnostic — see
[`examples/diagnostics/widening`](../diagnostics/README.md#widening).

## Running it

From the repo root:

```
go run ./cmd/servo graph --dir examples/scoped            # scope attribution in the graph
go run ./cmd/servo check --dir examples/scoped            # is servo_gen.go still fresh?
cd examples/scoped && go test -race ./...
```

## This module is the gate, not the demo

A golden file cannot catch an instance torn down while a caller still holds it, an acquirer
spinning against a dying entry, or a goroutine per key that outlives its key. `cmd/chat/scope_test.go`
is where scopes are actually verified, and it is the reason the feature is allowed to exist in the
generator: every test runs under `-race`, and every one ends in a `goleak` check.

The suite covers, among others:

| Test | What would otherwise go unnoticed |
| --- | --- |
| `TestConcurrentAcquireOfOneColdKeyConstructsOnce` | Two cold acquires both constructing, orphaning one entry and its goroutine |
| `TestEvictionRacingAcquire` | An acquirer that finds an entry the instant it decides to die, and spins on the corpse forever |
| `TestCancellationIsTheOnlyRelease` | A caller who forgets the release closure pinning an instance for the life of the process |
| `TestShutdownRacingAcquires` | A handler acquiring while `Shutdown` runs, hanging or getting a torn instance |
| `TestAcquireAfterShutdownNeverReturnsATornInstance` | The creator path handing back an instance already drained and stopped |
| `TestSharedKeyAcquireAfterShutdown` | The same, on the join path — the one the test above cannot reach, since it uses a distinct key per goroutine |
| `TestConstructorErrorLeavesNoEntry` | A failed construction poisoning the key for everyone after it |
| `TestManyDistinctKeys` | A goroutine per key outliving the key |
| `TestMaxCountsInstancesNotMapEntries` | `Max` bounding map size rather than live instances, so a slow drain lets the cap be exceeded |

`servotest.Linger(t, 0)` is what makes the timer-driven cases testable: it shrinks every scope's
linger window for one test, so an eviction thirty seconds away happens while the test is still
running. It sets a package variable, so those tests do not run in parallel.

CI runs this suite with `-count=5` on every pull request and every push to `master`, and
the full 200-run soak nightly.

## Further reading

- [Scoped instances](https://okian.github.io/servo/reference/scopes.html) — the reference page
- [Tutorial chapter 14](https://okian.github.io/servo/tutorial/14-scoped-instances.html) — a
  per-user session scope built from scratch
- [Limitations](https://okian.github.io/servo/limitations.html) — what scopes deliberately do not do
