# 17. Testing strategy

Every chapter so far has written tests alongside the code they cover — a repository test in
[chapter 5](05-repository-layer.md), a service test in [chapter 8](08-service-layer.md), a
full-graph test in [chapter 13](13-wiring-with-servo.md). None of that was incidental: by now the
service has 64 test functions across 19 files, and they don't all test the same thing the same way
on purpose. This chapter steps back from individual layers and looks at the whole shape — which
kind of test catches which kind of bug, why there are four distinct styles instead of one, and how
to run each of them on demand instead of always running everything.

One file gets its first full walkthrough here rather than a recap: `api/api_test.go`. Chapter 10
built the handlers and the middleware chain and proved them with a live `go run` and `curl`, but
deferred its httptest suite to this chapter, since it's really the second of the four testing
styles below, not one more handler concern.

## Four styles, not one

```mermaid
flowchart TB
    T1["Tier 1: Unit tests<br/>gomock + httptest.NewRecorder<br/>one component, no socket, no servo<br/>33 tests / 9 files"]
    T2["Tier 2: API-contract tests<br/>real socket + gomock<br/>full handler chain, real socket, no servo<br/>17 tests / 3 files"]
    T3["Tier 3: Full-graph tests<br/>servo.Override + NewTestApp + gomock<br/>the real wiring, fake infrastructure<br/>3 tests / 3 files"]
    T4["Tier 4: Integration tests<br/>real Postgres / Redis / NATS<br/>env-var gated, skips without infra<br/>11 tests / 4 files"]

    T1 -->|"faster, narrower"| T2 --> T3 --> T4
    T4 -->|"slower, broader, catches what mocks can't"| T1
```

Each tier trades speed for scope. A unit test runs in microseconds and pins down one function's
logic; it can't tell you whether the SQL in `postgres/postgres.go` is actually valid, because it
never touches a database. An integration test proves the SQL is valid, the driver handshake works,
and the schema migration ran — but it's slower, needs Docker, and a failure there says less about
*which* line is wrong. Neither one replaces the other; a service that only had one of these tiers
would either be fast and blind, or thorough and unable to run in under a second locally.

The four tiers, concretely:

| Tier | Technique | Real infra? | Real HTTP socket? | Real servo graph? | Files |
|---|---|---|---|---|---|
| 1. Unit | gomock, `httptest.NewRecorder` | No | No | No | `auth`, `config`, `service` (×2), `resilience` (×2), `observability` (×2), `session` |
| 2. API-contract | gomock, `httptest.NewServer` (`net.Listen` for gRPC) | No | Yes | No | `api/api_test.go`, and `ginapi`/`grpcapi` for the other two transports (ch 11 and 12) |
| 3. Full-graph | gomock via `servotest.PanicReporter`, `servo.Override`, `NewTestApp` | No | Yes | Yes | `cmd/orders/app_test.go`, plus one per transport variant |
| 4. Integration | none — real driver, real server | Yes | Yes (where relevant) | No | `postgres`, `redis`, `natsbroker`, `notifier` |

Tiers 1 and 4 were both introduced already — chapter 8 for the mock-based pattern that tiers 1 and
3 both build on, chapter 5 for the environment-variable-gated skip that all of tier 4 uses. Tier 3
was chapter 13's `NewTestApp`. What's new here is tier 2, and the practice of running these
selectively rather than as one undifferentiated `go test ./...`.

One tier-3 helper belongs to scopes specifically. `servotest.Linger(t, d)` shrinks every scope's
linger window for the duration of one test, the way `servotest.Timeout` shrinks the stop budget, so
an eviction that would otherwise be thirty seconds away happens while the test is still running.
Without it, asserting that an instance is actually torn down means either sleeping for the real
window or not asserting it at all. Generated code reads the override once per scope, inside `New`,
so call it *before* constructing the app; and because the underlying setting is a package variable,
a test using it must not run in parallel. [Chapter 14](14-scoped-instances.md) uses it for exactly
this.

## Tier 2: proving the HTTP contract, not just the handlers

Chapter 10's handlers were tested implicitly, by running the real service and curling it. That
proves the happy path once, by hand. `api/api_test.go` automates the same kind of check — a real
request over a real (loopback) socket, through the real middleware chain, against a real
`http.ServeMux` — but for every status code the API can return, not just the ones a manual `curl`
session happened to try.

The difference from tier 1's `httptest.NewRecorder` tests matters: `handler.ServeHTTP(httptest.NewRecorder(), req)` calls a handler as a plain Go function — useful for testing one middleware in
isolation (`resilience/ratelimit_test.go` does exactly this), but it never exercises routing,
never binds a real port, and never proves the middleware chain in `api.New` is actually assembled
in the order chapter 16 insists it must be. `httptest.NewServer` does all three: it starts a real
listener, and every test in this file talks to it the same way a real client would.

```go
func newTestServer(t *testing.T) (*httptest.Server, *mocks.MockOrderRepository, *auth.Issuer) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	users := mocks.NewMockUserRepository(ctrl)
	orderCache := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	// OrderService always tries the cache — a miss on read and a
	// best-effort write on create, regardless of which test is running, so
	// these two are set up once here rather than repeated in every test.
	orderCache.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, cache.ErrMiss).AnyTimes()
	orderCache.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// Each component gets its own narrow config, built as a literal. RPS
	// is set explicitly (rather than relying on the envDefault) because a
	// bare struct literal skips caarlos0/env's tag processing entirely —
	// every field not set here is the Go zero value, not the configured
	// default. A zero RPS would mean the rate limiter allows exactly one
	// request per test, ever; see
	// TestRateLimiterRejectsRequestsOverTheLimitAndCountsIt for the test
	// that actually wants that.
	authCfg := &auth.Config{Secret: "test-secret", Expiry: time.Hour}
	limitCfg := &resilience.Config{RPS: 1000}
	sessionCfg := &session.Config{Recent: 10}
	issuer := auth.New(authCfg)
	orders := service.New(repo, orderCache, pub, quietLogger())
	authSvc := service.NewAuthService(users, issuer)
	metrics := observability.NewMetrics()
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	testUser := &domain.User{ID: uuid.New(), Username: "alice", PasswordHash: hash}
	users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(testUser, nil).AnyTimes()
	users.EXPECT().GetByUsername(gomock.Any(), "nobody").Return(nil, domain.ErrNotFound).AnyTimes()

	srv := api.New(&api.Config{}, orders, authSvc, issuer, metrics, tracer,
		resilience.NewRateLimiter(limitCfg, metrics), newFakeSessions(sessionCfg), quietLogger())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, repo, issuer
}
```

This is plain Go construction — the same three or four lines main.go's generated `New` would
otherwise write — with servo nowhere in sight. That's deliberate, not a gap this chapter forgot to
fill: `servo.Override` and `NewTestApp` don't appear until tier 3, and this file exists specifically
to prove the HTTP contract on its own first, decoupled from whether the dependency graph wires up
correctly. Two details worth noticing:

- `RateLimitRPS: 1000` is set explicitly rather than left to `Config`'s own `envDefault`. A bare
  bare struct literal skips `caarlos0/env`'s tag processing entirely — every field not set
  here is Go's zero value, not the configured default. A zero `RPS` clamps the limiter's
  burst to 1, which silently broke `TestCreateOrderSucceedsWithValidToken` the first time the rate
  limiter was wired into `api.New` in chapter 16, well after this fixture was written. See
  Diagnostics below.
- `orderCache.EXPECT().Get(...).AnyTimes()` and the two other `AnyTimes()` expectations are set up
  once, here, rather than repeated in every test function, because `OrderService` always tries the
  cache on every read and always attempts a best-effort write on every create — that's true
  regardless of which specific test is running, so pinning it down once in the shared fixture
  keeps the test functions below from repeating three lines each.

The tests themselves each check one status code the API contract promises:

```go
func TestLoginSucceedsWithCorrectPassword(t *testing.T) { /* 200, non-empty token */ }
func TestLoginFailsWithWrongUsername(t *testing.T)      { /* 401 */ }
func TestCreateOrderRequiresAuth(t *testing.T)           { /* 401, no Authorization header */ }
func TestCreateOrderSucceedsWithValidToken(t *testing.T) { /* 201, echoes item/quantity back */ }
func TestGetOrderReturns404ForUnknownID(t *testing.T)    { /* 404 */ }
func TestGetOrderReturns403ForAnotherUsersOrder(t *testing.T) { /* 403 — see chapter 10 */ }
```

The seventh is not like the others:

```go
// This pins down a real bug from this chapter's own history: Run originally
// just called ListenAndServe and returned, with no select on ctx.Done() at
// all. servo's generated App.Run waits for every Runner before ever calling
// Shutdown, so that version hung forever on a real cancellation — nothing
// after Run() in main() ever ran, and the process never exited on SIGTERM.
// This test catches exactly that regression: it doesn't call Stop at all,
// so if Run ever again relies on Stop to make it return, it will time out.
func TestRunReturnsPromptlyWhenContextIsCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	apiCfg := &api.Config{HTTPAddr: "127.0.0.1:0"}
	limitCfg := &resilience.Config{RPS: 1000}
	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})
	orders := service.New(mocks.NewMockOrderRepository(ctrl), mocks.NewMockOrderCache(ctrl), mocks.NewMockEventPublisher(ctrl))
	authSvc := service.NewAuthService(mocks.NewMockUserRepository(ctrl), issuer)
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	testMetrics := observability.NewMetrics()
	srv := api.New(apiCfg, orders, authSvc, issuer, testMetrics, tracer,
		resilience.NewRateLimiter(limitCfg, testMetrics), newFakeSessions(&session.Config{Recent: 10}), quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation")
	}
}
```

This is worth dwelling on because it's a real, previously-shipped bug caught this way, not a
hypothetical example invented for this chapter. `Server.Run` originally called
`s.http.ListenAndServe()` and returned whatever it returned — with no `select` on `ctx.Done()` at
all. Under servo's generated `App.Run`, every `Runner`'s `Run` is expected to return once its
context is cancelled; `Shutdown` isn't called until they all have. A `Run` that only returns when
the listener itself fails hangs forever on an ordinary `SIGTERM`. This test reproduces the failure
directly: it cancels the context and asserts `Run` returns within two seconds, without ever calling
`Stop`. Revert the fix in `api/server.go` and this is the test that goes red — not
`TestCreateOrderSucceedsWithValidToken`, which would still pass, because it never exercises shutdown
at all. That's the point of writing a regression test narrowly: a broad test that happens to also
catch a bug tells you less about *why* it failed than a test built to fail exactly one way.

## Try it yourself

```
$ go test ./api/... -v -count=1
=== RUN   TestLoginSucceedsWithCorrectPassword
--- PASS: TestLoginSucceedsWithCorrectPassword (0.12s)
=== RUN   TestLoginFailsWithWrongUsername
--- PASS: TestLoginFailsWithWrongUsername (0.05s)
=== RUN   TestCreateOrderRequiresAuth
--- PASS: TestCreateOrderRequiresAuth (0.05s)
=== RUN   TestCreateOrderSucceedsWithValidToken
--- PASS: TestCreateOrderSucceedsWithValidToken (0.09s)
=== RUN   TestGetOrderReturns404ForUnknownID
--- PASS: TestGetOrderReturns404ForUnknownID (0.09s)
=== RUN   TestGetOrderReturns403ForAnotherUsersOrder
--- PASS: TestGetOrderReturns403ForAnotherUsersOrder (0.09s)
=== RUN   TestRunReturnsPromptlyWhenContextIsCancelled
--- PASS: TestRunReturnsPromptlyWhenContextIsCancelled (0.00s)
=== RUN   TestRecentRemembersWhatThisUserViewed
--- PASS: TestRecentRemembersWhatThisUserViewed (0.09s)
=== RUN   TestRecentRejectsAnUnauthenticatedCaller
--- PASS: TestRecentRejectsAnUnauthenticatedCaller (0.05s)
=== RUN   TestRecentIsEmptyForANewSession
--- PASS: TestRecentIsEmptyForANewSession (0.09s)
=== RUN   TestAdminEndpointsAreNotOnThePublicListener
--- PASS: TestAdminEndpointsAreNotOnThePublicListener (0.06s)
PASS
ok  	example.com/servoorders/api	0.969s
```

No request logs appear between the `--- PASS` lines, and that is deliberate: the fixture passes a
`quietLogger()` — `slog.New(slog.DiscardHandler)` wrapped in an `observability.Logger` — into
`api.New`. The middleware still runs, and still logs; it logs into a discard handler. Because the
logger is injected rather than global ([chapter 15](15-observability.md)), silencing it in a test
is a value you pass, not a package-level default you have to swap and restore.

Now the whole suite, tier by tier. `make test` runs tiers 1 through 3 — nothing in them touches
real infrastructure, so nothing here needs Docker running:

```
$ make test
go test ./...
?   	example.com/servoorders/admin	[no test files]
ok  	example.com/servoorders/api	1.054s
ok  	example.com/servoorders/auth	0.606s
?   	example.com/servoorders/broker	[no test files]
?   	example.com/servoorders/cache	[no test files]
ok  	example.com/servoorders/cmd/orders	0.583s
ok  	example.com/servoorders/cmd/ordersgin	1.185s
ok  	example.com/servoorders/cmd/ordersgrpc	0.873s
ok  	example.com/servoorders/config	0.500s
?   	example.com/servoorders/domain	[no test files]
ok  	example.com/servoorders/ginapi	1.480s
ok  	example.com/servoorders/grpcapi	0.645s
?   	example.com/servoorders/grpcapi/ordersv1	[no test files]
?   	example.com/servoorders/migrations	[no test files]
?   	example.com/servoorders/mocks	[no test files]
ok  	example.com/servoorders/natsbroker	0.505s
ok  	example.com/servoorders/notifier	0.344s
ok  	example.com/servoorders/observability	0.569s
?   	example.com/servoorders/openapi	[no test files]
ok  	example.com/servoorders/postgres	0.522s
ok  	example.com/servoorders/redis	0.508s
?   	example.com/servoorders/repository	[no test files]
ok  	example.com/servoorders/resilience	0.384s
ok  	example.com/servoorders/service	0.719s
ok  	example.com/servoorders/session	0.297s
```

Notice `postgres`, `redis`, and `natsbroker` all say `ok`, not `[no test files]` — they have test
files, but every function in them checked its `TEST_*` environment variable, found it unset, and
called `t.Skip`. A skipped test still reports `ok`; nothing here proves the repository layer
actually talks to Postgres yet. That requires `make up` (bringing up the real
`docker-compose.yml` stack from [chapter 19](19-running-and-deployment.md)) followed by:

```
$ make test-integration
TEST_POSTGRES_DSN="postgres://orders:orders@localhost:5432/orders?sslmode=disable" \
TEST_REDIS_ADDR="localhost:6379" \
TEST_NATS_URL="nats://localhost:4222" \
go test ./... -v
...
=== RUN   TestCreateAndGetOrder
--- PASS: TestCreateAndGetOrder (0.02s)
=== RUN   TestGetMissingOrderReturnsErrNotFound
--- PASS: TestGetMissingOrderReturnsErrNotFound (0.01s)
=== RUN   TestListByUserOrdersMostRecentFirst
--- PASS: TestListByUserOrdersMostRecentFirst (0.02s)
=== RUN   TestGetByUsernameFindsSeededUser
--- PASS: TestGetByUsernameFindsSeededUser (0.01s)
=== RUN   TestGetByUsernameUnknownReturnsErrNotFound
--- PASS: TestGetByUsernameUnknownReturnsErrNotFound (0.01s)
PASS
ok  	example.com/servoorders/postgres	0.229s
=== RUN   TestGetOnEmptyKeyReturnsErrMiss
--- PASS: TestGetOnEmptyKeyReturnsErrMiss (0.01s)
=== RUN   TestSetThenGetRoundTrips
--- PASS: TestSetThenGetRoundTrips (0.01s)
=== RUN   TestInvalidateRemovesTheKey
--- PASS: TestInvalidateRemovesTheKey (0.00s)
PASS
ok  	example.com/servoorders/redis	0.165s
```

Same command, same test binaries, same `go test ./...` — the only thing that changed is three
environment variables, and the tests that were skipping now actually run. Nothing needed a build
tag or a separate file. This is worth calling out because it's easy to assume `go test`'s result
caching would get in the way — run the command once, then again with different env vars, expecting
a stale cached "pass" instead of a real re-run. It doesn't: Go's build cache records which
environment variables a test actually read via `os.Getenv` and keys the cached result on their
values, specifically to make this safe. Toggling `TEST_POSTGRES_DSN` invalidates the cache for
every test that checked it, and only those.

## Diagnostics

- **A test fails on its second HTTP call, never its first, and only after some unrelated chapter's
  change** — check whether a bare config literal in the failing test's fixture is missing
  `RPS`. A struct literal doesn't run through `caarlos0/env`, so an omitted field is Go's
  zero value, not the configured default; zero `RPS` clamps the token bucket's burst to 1,
  and the first request in a test consumes it.
- **A `t.Setenv(k, "")` doesn't produce the "required environment variable" error you expected** —
  an empty string is still a value as far as `,required` validation is concerned; it doesn't unset
  anything. Use `os.Unsetenv` (with `os.LookupEnv` first, so a real ambient value can be restored in
  `t.Cleanup`) to actually simulate a missing variable.
- **`postgres`/`redis`/`natsbroker` tests report `ok` in CI, but nobody's sure they're doing
  anything** — check that the job actually sets `TEST_POSTGRES_DSN`/`TEST_REDIS_ADDR`/
  `TEST_NATS_URL` (see [chapter 18](18-cicd.md)). A missing `services:` block or a typo'd env var
  name produces a suite that passes by skipping everything, silently.
- **`gomock.NewController(t)` panics with "missing call"** — an `EXPECT()` was set up but the
  mocked method was never actually called before the test function returned and `ctrl.Finish()` ran
  (implicitly, via `t.Cleanup`, since gomock v1.5+). Either the code path under test didn't reach
  that call, or the expectation belongs on a different mock than the one it was set on.
- **A tier 3 (`NewTestApp`) test either crashes with a stack trace mentioning `PanicReporter`, or
  quietly returns a `500` where a real handler bug would be the more obvious suspect** — both are
  the same root cause wearing different clothes. A `*testing.T` isn't reachable from inside
  `NewTestApp`'s generated graph, so unmet or unexpected mock calls panic instead of calling
  `t.Fatal` — and whether that panic crashes the process or gets silently absorbed into a `500`
  depends on whether it fired inside a request `recoverMiddleware` was already wrapping, or outside
  one (typically during `t.Cleanup`'s `ctrl.Finish()`). See chapter 13's diagnostics for both cases
  and how to read either one back to the specific mock that caused it.

## Do's and don'ts

- **Do** pick the cheapest tier that can actually catch the bug you're worried about. A business
  logic edge case belongs in tier 1; a "does the SQL actually run" question belongs in tier 4.
  Writing a tier-4 test for something tier 1 could catch just makes the suite slower for no extra
  confidence.
- **Do** write a regression test that fails in exactly one specific way, like
  `TestRunReturnsPromptlyWhenContextIsCancelled` — a test built to catch one bug tells you which
  one broke when it goes red. A broad test that happens to also catch the same bug tells you less.
- **Do** let tier 4 tests skip, not fail, when their infrastructure isn't there. A test suite that
  hard-fails without Docker running makes `go test ./...` unsafe to run casually, which is exactly
  when you want it to be safest to run.
- **Don't** reach for `NewTestApp`/`servo.Override` (tier 3) to test business logic. It exists to
  answer "does the real wiring hold together," not "is this `if` statement correct" — `service`'s
  own gomock-based tests (tier 1) already answer that, faster, with a real `*testing.T` and normal
  `t.Fatal` failures instead of panics.
- **Don't** let `httptest.NewRecorder` tests and `httptest.NewServer` tests blur together. The
  former calls a handler as a function; the latter proves routing and middleware ordering over a
  real socket. A middleware bug in how `api.New` assembles the chain (chapter 16's `r.Pattern`
  bug, for instance) is only visible to the latter.
- **Don't** assume `go test`'s caching will silently hide an integration test from a second run —
  and don't disable caching reflexively either (`-count=1` everywhere). It's slower for no benefit
  once you know the cache key already accounts for the environment variables that matter here.

## Alternatives

- **testcontainers-go instead of `docker compose` + env vars.** Tier 4 here assumes the reader
  starts Postgres/Redis/NATS themselves (locally via `make up`, in CI via `services:` — chapter
  17). [testcontainers-go](https://golang.testcontainers.org/) starts and stops containers from
  inside the test process itself, so `go test ./...` alone is sufficient with no external `make up`
  step first. That convenience costs a heavier per-package test binary (each package pulls in the
  Docker client) and slower individual test runs (spinning up a container per test or per package,
  rather than once for the whole suite) — a reasonable trade for a larger team where "did you
  remember to start the stack" is a recurring source of red CI, less obviously worth it for a
  single small service.
- **Table-driven tests instead of one function per case.** `service/service_test.go` and others
  here use one `func Test...` per behavior rather than a single table-driven test with subtests.
  Table-driven tests reduce repetition when many cases share the same shape; separate functions
  read better in a failure list (`--- FAIL: TestGetOrderRejectsAnotherUsersOrder` is immediately
  legible; `--- FAIL: TestOrderService/case_3` sends you back to the table to find out what case 3
  was). Either is a reasonable default — this service picked separate functions because most of
  its test cases don't actually share enough shape to make a table clearly simpler.
- **Contract tests against a schema (e.g. Pact) instead of `api_test.go`'s own httptest suite** —
  worth considering once more than one team owns a client of this API and "does the response shape
  still match what the client expects" becomes a cross-team question, not just an internal one. For
  a single-team service, hand-written httptest assertions against the same DTOs the handlers
  actually use cost less to maintain and catch the same class of drift.

## Next

[Chapter 18: CI/CD](18-cicd.md) — turning `make test` and `make test-integration` into a GitHub
Actions workflow that runs both automatically, plus the build and `servo check` steps that don't
have a `make` target yet.
