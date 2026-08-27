# 11. Wiring with servo

Nine packages exist now, each with its own constructor, and nothing connecting them yet — no
`main.go` that constructs a logger-shaped thing then a database then a cache then a service then a
server, checking errors and unwinding partial construction at every step. That's the file this
chapter doesn't write by hand.

## Declare the graph

Create `cmd/orders/spec.go`, gated by the `servoinject` build tag so it never compiles into the
real binary:

```go
//go:build servoinject

package main

import (
	"example.com/servoorders/api"
	"example.com/servoorders/broker"
	"example.com/servoorders/cache"
	"example.com/servoorders/natsbroker"
	"example.com/servoorders/notifier"
	"example.com/servoorders/postgres"
	"example.com/servoorders/redis"
	"example.com/servoorders/repository"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*notifier.Notifier](),

		servo.Bind[repository.OrderRepository, *postgres.Store](),
		servo.Bind[repository.UserRepository, *postgres.Store](),
		servo.Bind[cache.OrderCache, *redis.Cache](),
		servo.Bind[broker.EventPublisher, *natsbroker.Publisher](),
	)
}
```

Two roots: `api.Server` (everything the HTTP layer needs, transitively) and `notifier.Notifier`
(which nothing else depends on, so without declaring it a root, `servo` would never know to build
it at all — an unreferenced constructor is just dead code as far as the graph is concerned).

## A diagnostic worth triggering on purpose

Before adding the four `servo.Bind` lines above, try generating without them — just the two roots.
(This section also uses `mocks/servo_adapters.go`, written later in this same chapter — if you're
following in order and haven't created it yet, you'll see two implementers instead of three below,
which makes exactly the same point.)

```
$ go run github.com/okian/servo/v3/cmd/servo generate --dir .
example.com/servoorders/cmd/orders: servo: 4 diagnostic(s):

.../service/service.go:27:6: servo: no provider for example.com/servoorders/repository.OrderRepository
  needed by *example.com/servoorders/service.OrderService  .../service/service.go:27:6
  needed by *example.com/servoorders/api.Server            .../api/server.go:23:6
  root                                                      .../cmd/orders/spec.go:13:3

  3 types implement example.com/servoorders/repository.OrderRepository — add one of:
      servo.Bind[example.com/servoorders/repository.OrderRepository, *example.com/servoorders/mocks.MockOrderRepository]()      .../mocks/repository_mock.go:34:6
      servo.Bind[example.com/servoorders/repository.OrderRepository, *example.com/servoorders/mocks.OrderRepositoryForServo]()      .../mocks/servo_adapters.go:24:6
      servo.Bind[example.com/servoorders/repository.OrderRepository, *example.com/servoorders/postgres.Store]()      .../postgres/postgres.go:31:6
```

(Three more diagnostics follow, identical in shape, for `UserRepository`, `OrderCache`, and
`EventPublisher`.) This is worth actually seeing rather than taking on faith, and it's worth
noticing there are *three* candidates, not two: `mocks.MockOrderRepository` (gomock's own
generated mock, from [chapter 8](08-service-layer.md)) and `mocks.OrderRepositoryForServo` (the
wrapper this chapter adds further down, for `servo.Override`) both structurally satisfy
`repository.OrderRepository` exactly as well as `postgres.Store` does. `servo` has no way to know
which one you meant "for real," and it's right not to guess. The four `Bind` lines above aren't
optional the way they might look in a module with only one implementation of each interface —
once even one mock exists anywhere in the module, an explicit `Bind` is what makes generation
succeed at all.

## Generate, and read what came back

With the binds in place:

```
$ go run github.com/okian/servo/v3/cmd/servo generate --dir .
```

No output — success is silent, matching every other servo command. `cmd/orders/servo_gen.go` now
exists, starting with a resolved-graph comment that's worth reading end to end before the code
below it:

```
//	[L1] *example.com/servoorders/config.Config
//	      deps: none
//	      capabilities: none | binding: sole candidate | config/config.go:39:6
//	[L2] *example.com/servoorders/postgres.Store
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: Initializer, Finalizer, Healther | binding: explicit bind | postgres/postgres.go:31:6
//	[L2] *example.com/servoorders/redis.Cache
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: Initializer, Finalizer, Healther | binding: explicit bind | redis/redis.go:30:6
//	[L2] *example.com/servoorders/natsbroker.Publisher
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: Initializer, Finalizer, Healther | binding: explicit bind | natsbroker/natsbroker.go:26:6
//	[L3] *example.com/servoorders/service.OrderService
//	      deps: *example.com/servoorders/postgres.Store, *example.com/servoorders/redis.Cache, *example.com/servoorders/natsbroker.Publisher
//	      capabilities: none | binding: sole candidate | service/service.go:27:6
//	[L2] *example.com/servoorders/auth.Issuer
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: none | binding: sole candidate | auth/auth.go:28:6
//	[L3] *example.com/servoorders/service.AuthService
//	      deps: *example.com/servoorders/postgres.Store, *example.com/servoorders/auth.Issuer
//	      capabilities: none | binding: sole candidate | service/auth_service.go:18:6
//	[L4] *example.com/servoorders/api.Server
//	      deps: *example.com/servoorders/config.Config, *example.com/servoorders/service.OrderService, *example.com/servoorders/service.AuthService, *example.com/servoorders/auth.Issuer
//	      capabilities: Runner, Finalizer | binding: sole candidate | api/server.go:23:6
//	[L2] *example.com/servoorders/notifier.Notifier
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: Runner | binding: sole candidate | notifier/notifier.go:23:6
```

## Capabilities, side by side

Nine chapters built these components one at a time; here's every capability every one of them
ended up with, in one place, and *why* each is what it is:

| Type | Capabilities | Why |
|---|---|---|
| `config.Config` | none | Pure data, validated once at construction |
| `postgres.Store` | Initializer, Finalizer, Healther | Connects, disconnects, and can report a real health check |
| `redis.Cache` | Initializer, Finalizer, Healther | Same shape as Store — connect/disconnect/health |
| `natsbroker.Publisher` | Initializer, Finalizer, Healther | Same shape again |
| `service.OrderService` | none | Pure orchestration logic, nothing to start or stop |
| `auth.Issuer` | none | Pure JWT/hashing logic |
| `service.AuthService` | none | Pure orchestration logic |
| `api.Server` | Runner, Finalizer | Serves until told to stop ([chapter 10](10-api-layer.md)'s `Run` bug lives here); `Stop` closes the listener |
| `notifier.Notifier` | Runner (no Finalizer) | Its own cleanup (`conn.Drain()`) happens via `defer` inside `Run` itself when `ctx` cancels, not a separate `Stop` |

Every one of these is detected structurally — `types.Implements`, checked at generation time.
Nothing in any of these nine packages imports `servo` except the spec file itself. `notifier`
having *no* `Stop` isn't a gap: it genuinely doesn't need one, and servo doesn't require every
component to implement every capability, or even any of them.

## New, Run, and Shutdown

```go
func New(ctx context.Context) (*App, error) {
	a := &App{}

	config, err := config.New()
	if err != nil {
		return nil, err
	}
	a.config = config

	store, err := postgres.New(config)
	if err != nil {
		return nil, err
	}
	a.store = store
	// ... cache, publisher, orderService, issuer, authService, server, notifier ...

	{
		var timingMu sync.Mutex
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			start := time.Now()
			err := a.store.Init(gctx)
			timingMu.Lock()
			a.startupReport.Nodes = append(a.startupReport.Nodes, servo.StartupNode{Type: "*example.com/servoorders/postgres.Store", Duration: time.Since(start)})
			timingMu.Unlock()
			return err
		})
		// ... cache.Init, publisher.Init, run concurrently in the same errgroup ...
		if err := g.Wait(); err != nil {
			report := a.Shutdown(ctx)
			return nil, errors.Join(err, report)
		}
	}
	return a, nil
}
```

Construction happens in dependency order — `config` before anything that needs it, `store` before
`orderService`. `Init` calls for the three Initializers run *concurrently*, in one `errgroup`,
since none of them depend on each other (all three only depend on `config`, which is already
built) — servo doesn't serialize work that has no reason to be serial. If any `Init` fails, `New`
calls `Shutdown` itself before returning, so a failed startup never leaves the components that
*did* succeed connected with nothing tracking them.

`Run` launches every `Runner` and waits for all of them — this is exactly the errgroup shown in
[chapter 10](10-api-layer.md#run-and-stop--and-a-bug-worth-hitting-on-purpose)'s postmortem, so
its behavior should already feel familiar rather than new. `Shutdown` runs in *reverse* order —
`api.Server` first (stop accepting new work before tearing down what it depends on), then the
three infrastructure Finalizers — each one exactly once, via `sync.Once`, so a second `Shutdown`
call (or two concurrent ones) is safe.

## Check it, graph it, ask it questions

```
$ go run github.com/okian/servo/v3/cmd/servo check --dir .
```

Silent, same as generate — `check` re-resolves and re-emits in memory, diffs against what's
committed, and only prints something if they disagree.

```
$ go run github.com/okian/servo/v3/cmd/servo graph --dir . --format=mermaid
graph BT
  n0["*example.com/servoorders/config.Config"]:::level1
  n1["*example.com/servoorders/postgres.Store"]:::level2
  n2["*example.com/servoorders/redis.Cache"]:::level2
  n3["*example.com/servoorders/natsbroker.Publisher"]:::level2
  n4["*example.com/servoorders/service.OrderService"]:::level3
  n5["*example.com/servoorders/auth.Issuer"]:::level2
  n6["*example.com/servoorders/service.AuthService"]:::level3
  n7["*example.com/servoorders/api.Server"]:::level4
  n8["*example.com/servoorders/notifier.Notifier"]:::level2
  n1 --> n0
  n2 --> n0
  n3 --> n0
  n4 --> n1
  n4 --> n2
  n4 --> n3
  n5 --> n0
  n6 --> n1
  n6 --> n5
  n7 --> n0
  n7 --> n4
  n7 --> n6
  n7 --> n5
  n8 --> n0
  classDef level1 fill:#bfdbfe;
  classDef level2 fill:#93c5fd;
  classDef level3 fill:#60a5fa;
  classDef level4 fill:#3b82f6;
```

That's the complete, unedited output — the `classDef` lines are what give each level its own
shade when rendered. Here it is rendered, with the full import paths shortened to just the type
name so it's actually readable at a glance:

```mermaid
graph BT
  n0["config.Config"]:::level1
  n1["postgres.Store"]:::level2
  n2["redis.Cache"]:::level2
  n3["natsbroker.Publisher"]:::level2
  n4["service.OrderService"]:::level3
  n5["auth.Issuer"]:::level2
  n6["service.AuthService"]:::level3
  n7["api.Server"]:::level4
  n8["notifier.Notifier"]:::level2
  n1 --> n0
  n2 --> n0
  n3 --> n0
  n4 --> n1
  n4 --> n2
  n4 --> n3
  n5 --> n0
  n6 --> n1
  n6 --> n5
  n7 --> n0
  n7 --> n4
  n7 --> n6
  n7 --> n5
  n8 --> n0
  classDef level1 fill:#bfdbfe;
  classDef level2 fill:#93c5fd;
  classDef level3 fill:#60a5fa;
  classDef level4 fill:#3b82f6;
```

And a targeted question — why does `postgres.Store` exist at all, from the graph's perspective:

```
$ go run github.com/okian/servo/v3/cmd/servo why --dir . postgres.Store
root  *example.com/servoorders/api.Server
  -> *example.com/servoorders/service.OrderService
  -> *example.com/servoorders/postgres.Store
```

## Testing the whole thing without any of it running

Everything up to this point still needs real Postgres, Redis, and NATS to actually construct.
`servo.Override` changes that, for tests specifically — add it to `spec.go`, alongside the binds:

```go
servo.Override[repository.OrderRepository, *mocks.OrderRepositoryForServo](),
servo.Override[repository.UserRepository, *mocks.UserRepositoryForServo](),
servo.Override[cache.OrderCache, *mocks.OrderCacheForServo](),
servo.Override[broker.EventPublisher, *mocks.EventPublisherForServo](),
```

The gomock mocks from chapter 8 can't be used directly here: `NewMockOrderRepository(ctrl
*gomock.Controller)` needs a `*gomock.Controller`, which itself needs something implementing
`gomock.TestReporter` — and there's no `*testing.T` reachable from inside a generated graph.
`servotest.PanicReporter` (from servo's own `servotest` package) supplies one without pulling
gomock into servo's own `servotest` at all — the same pattern servo's `examples/mocking/gomock`
already establishes. Create `mocks/servo_adapters.go`:

```go
package mocks

import (
	"go.uber.org/mock/gomock"

	"github.com/okian/servo/v3/servotest"
)

type OrderRepositoryForServo struct {
	*MockOrderRepository
	Finish func()
}

func NewOrderRepositoryForServo() *OrderRepositoryForServo {
	ctrl := gomock.NewController(servotest.PanicReporter{})
	return &OrderRepositoryForServo{MockOrderRepository: NewMockOrderRepository(ctrl), Finish: ctrl.Finish}
}

// UserRepositoryForServo, OrderCacheForServo, and EventPublisherForServo
// follow the exact same three-line shape, one per remaining interface.
```

Regenerating now also produces `cmd/orders/servo_gen_test.go` — a `NewTestApp` with the same
shape as `New`, except the four overridden dependencies are the zero-arg `*ForServo` wrappers
instead of `postgres.New`/`redis.New`/`natsbroker.New`.

Two things about `NewTestApp` are easy to assume and both wrong — worth stating plainly rather
than letting you find out by confusion:

- **`notifier.Notifier` still needs a real NATS connection**, even here. It was built in
  [chapter 7](07-messaging-layer.md) to open its own connection directly rather than going through
  `broker.EventPublisher` — a deliberate choice at the time, to demonstrate the consuming side of
  messaging independently — but it means `notifier` isn't one of the four interfaces `Override`
  touches. Calling `TestApp.Run(ctx)` would still try to reach real NATS. The test below never
  calls `Run` for exactly this reason.
- **`config.Config` still requires every one of its required environment variables**, even the
  ones whose real values are about to go unused. `POSTGRES_DSN`, `REDIS_ADDR`, and `NATS_URL` are
  still validated as present — `Config` isn't behind an interface, so `Override` has nothing to
  substitute for it. `JWT_SECRET` is the one value that actually matters here, since
  `auth.Issuer` is real and unmocked.

With both of those understood, the test itself is straightforward — construct, expect, hit the
HTTP handler directly:

```go
func TestFullAPIFlowWithMockedInfrastructure(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "unused-in-this-test")
	t.Setenv("REDIS_ADDR", "unused-in-this-test")
	t.Setenv("NATS_URL", "unused-in-this-test")
	t.Setenv("JWT_SECRET", "test-secret")

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() {
		app.userRepositoryForServo.Finish()
		app.orderRepositoryForServo.Finish()
		app.orderCacheForServo.Finish()
		app.eventPublisherForServo.Finish()
	})

	hash, _ := auth.HashPassword("password123")
	testUser := &domain.User{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Username: "alice", PasswordHash: hash}
	app.userRepositoryForServo.EXPECT().GetByUsername(gomock.Any(), "alice").Return(testUser, nil)
	app.orderCacheForServo.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil)
	app.orderRepositoryForServo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	app.eventPublisherForServo.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil)

	ts := httptest.NewServer(app.server.Handler())
	defer ts.Close()

	// ... POST /auth/login, then POST /orders with the returned token ...

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Errorf("Shutdown not clean: %v", r)
	}
}
```

`app.server`, `app.userRepositoryForServo`, and friends are all unexported fields — reachable here
only because this test file lives in `package main`, the same package `servo_gen_test.go` does,
exactly like servo's own `examples/basic/cmd/basic/app_test.go` reaches into its generated `App`
the same way.

```
$ go test ./cmd/orders/... -v
=== RUN   TestFullAPIFlowWithMockedInfrastructure
--- PASS: TestFullAPIFlowWithMockedInfrastructure (0.12s)
PASS
ok  	example.com/servoorders/cmd/orders	0.521s
```

Every layer from [chapter 5](05-repository-layer.md) through [chapter 10](10-api-layer.md), wired
exactly the way `main.go` wires it for real, running in well under a second, with zero containers.

## Diagnostics

- **`servo: N diagnostic(s)` listing two implementers of the same interface** — as demonstrated
  above, this is what happens the moment a mock and a real implementation coexist in one module
  with no explicit `Bind`. The fix is always the same: add the `servo.Bind[...]()` the error
  message already suggests.
- **`NewTestApp` fails with a "required environment variable" error** — see the config nuance
  above; every required field still needs a value in a test, even an unused placeholder string for
  the three that won't actually be dialed.
- **A test calling `TestApp.Run` hangs or fails to connect** — `notifier` isn't behind an
  overridden interface; don't call `Run` in a test that has no real NATS available. Test the HTTP
  surface directly through `app.server.Handler()` instead, as shown above.
- **`servo check` reports drift right after a manual edit to `servo_gen.go`** — expected; the file
  is marked `DO NOT EDIT` for exactly this reason. Change the source, not the generated output, and
  regenerate.

## Do's and don'ts

- **Do** add `servo.Bind` for an interface as soon as a second structural implementer appears
  anywhere in the module — including a mock. Waiting for the diagnostic to tell you is fine;
  treating it as a bug in `servo` instead of a real ambiguity is not.
- **Do** keep `spec.go` as the *only* file that imports `servo` (besides generated output). If a
  second file starts importing it, that's usually a sign some wiring logic escaped the spec file.
- **Don't** hand-edit `servo_gen.go` or `servo_gen_test.go`, ever, even for a "quick" fix — the
  next `servo generate` silently overwrites it, and `servo check` in CI exists specifically to
  catch a hand-edited file that's drifted from what the real source would produce.
- **Don't** add a root for something nothing else needs unless you actually want it constructed
  and run — an orphaned root is a real, if harmless, way to accidentally start something (a
  listener, a connection) that was only meant to exist as a library.

## Next

[Chapter 12: Observability](12-observability.md) — structured logs, metrics, and tracing, now that
there's a fully wired app to instrument.
