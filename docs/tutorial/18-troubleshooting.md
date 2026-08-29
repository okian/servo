# 18. Troubleshooting

Every chapter from 2 through 17 ended with its own `## Diagnostics` section, scoped to whatever
that chapter had just built. That's the right place to *learn* why something fails — the
explanation there assumes you have that chapter's context fresh. It's the wrong place to *find*
something once you've forgotten which chapter it was in. This chapter is the second one: every
diagnostic from this tutorial, in one place, organized by what you're actually looking at when
something goes wrong rather than by which layer produced it. Search this page for a phrase from
your actual error message rather than reading it top to bottom — that's what it's for.

Each entry is intentionally short. Follow the chapter link for the full explanation; this page's
job is to get you to the right one fast, not to repeat it.

## Getting started

- **`go: github.com/okian/servo/v3@...: reading github.com/okian/...: 404 Not Found`** — you're
  following along outside the servo repo without a real dependency to point at. See [chapter
  2](02-project-setup.md).
- **`make: docker: command not found`** — install Docker; nothing from chapter 5 onward works
  without it. See [chapter 2](02-project-setup.md).

## The process won't start

- **`env: required environment variable "X" is not set`** — working as intended; set the
  variable. Don't add a default just to silence this unless that default is safe everywhere this
  ever runs. See [chapter 3](03-configuration.md).
- **A `time.Duration` field fails to parse from an env var** — it needs a Go duration string
  (`"1h30m"`), not a bare number. See [chapter 3](03-configuration.md).
- **`servo: N diagnostic(s)` listing two implementers of the same interface** — a mock and a real
  implementation coexisting with no explicit `Bind`. Add the `servo.Bind[...]()` the message
  suggests. See [chapter 11](11-wiring-with-servo.md).
- **`servo check` reports drift right after editing `servo_gen.go` by hand** — expected; that file
  is generated and marked `DO NOT EDIT`. Change the source and regenerate. See [chapter
  11](11-wiring-with-servo.md).

## A dependency won't connect

- **`postgres: ping: failed to connect ... connection refused`** — Postgres isn't running or isn't
  where `POSTGRES_DSN` says. Confirm `make up` succeeded. See [chapter 5](05-repository-layer.md).
- **`redis: get: dial tcp ...: connect: connection refused`** — same idea, for Redis and
  `REDIS_ADDR`. `redis-cli -h localhost ping` is the fastest independent check. See [chapter
  6](06-caching-layer.md).
- **`nats: no responders`** — nothing is subscribed to the subject yet; a real race if `notifier`
  (or a test's own subscription) hasn't started before the publish. See [chapter
  7](07-messaging-layer.md).
- **`could not create directory ".../pg_wal": No space left on device`**, or `docker build`/`docker
  compose up --build` failing the same way — Docker Desktop's own VM disk is full, usually from
  *other* projects' volumes, not your host disk. See [chapter 17](17-running-and-deployment.md)'s
  full walkthrough of diagnosing and safely reclaiming this.
- **The `orders` container fails to start even though `postgres`/`redis`/`nats` all show `Up`** —
  check for `(healthy)` specifically, not just `Up`. See [chapter 17](17-running-and-deployment.md).

## Requests return the wrong status code

- **A `401` you didn't expect** — check the `Authorization` header is exactly `Bearer <token>`,
  one space, case-sensitive `Bearer`. See [chapter 10](10-api-layer.md). If it's a login attempt,
  a wrong password and an unknown username are *deliberately* both `401` — see [chapter
  9](09-authentication.md).
- **A `403` where you expected `404`, or vice versa** — this codebase returns `403` for "exists,
  but not yours" rather than hiding the order's existence behind a `404`, a deliberate and
  debatable choice for a resource whose ID isn't itself sensitive. See [chapter
  10](10-api-layer.md)'s note on when the other choice is more defensible.
- **A `500` where you expected a clean `4xx`** — check whether a domain sentinel error is actually
  reaching `writeDomainError`, or whether something is returning a bare, unwrapped error instead.
  See [chapter 10](10-api-layer.md). If this is inside a `NewTestApp`-based test specifically, see
  "Tests fail in confusing ways" below — a mock panic can also produce this.
- **`pgx.ErrNoRows` leaking out as a generic `500` instead of a `404`** — the repository method is
  missing its `errors.Is(err, pgx.ErrNoRows)` check. See [chapter 5](05-repository-layer.md).
- **`missing or malformed Authorization header` on a request you're sure has a token** — see the
  `401` entry above; this is the same header-format issue.

## The process won't shut down cleanly

- **A client (or `docker compose down`) hangs waiting for the process to exit after Ctrl+C /
  SIGTERM** — a `Run` method that doesn't `select` on `<-ctx.Done()` (directly, or via something
  like `api.Server`'s pattern) blocks forever once nothing else calls `Stop`. See [chapter
  10](10-api-layer.md) for the exact bug this tutorial shipped and fixed once.
- **A test calling `TestApp.Run` hangs or fails to connect** — `notifier` isn't behind an
  overridden interface, so `Run` still tries to reach real NATS. Test the HTTP surface via
  `app.server.Handler()` instead. See [chapter 11](11-wiring-with-servo.md).

## Data looks wrong, stale, or missing

- **Migrations show "already applied" but the schema looks wrong** — the tracking table only
  records that a file *ran*, not that it's still correct. Editing an already-applied migration file
  does nothing; add a new one instead. See [chapter 5](05-repository-layer.md).
- **Stale data after a value should have changed** — the most likely gap in any future update path:
  something wrote to Postgres but forgot to also call `Set` or `Invalidate` on the cache. See
  [chapter 6](06-caching-layer.md).
- **An event arrives twice for one order** — shouldn't happen with this tutorial's single `Publish`
  call per `CreateOrder`, but expected the moment anything at-least-once (an outbox poller, a retry
  loop) is added. See [chapter 7](07-messaging-layer.md).

## A session behaves like a singleton (or doesn't exist)

- **`servo: X is scoped, but Y is a singleton that depends on it`** — a component took the scoped
  type directly instead of the accessor interface. It compiles and every single-user test passes;
  in production the first user's session becomes everyone's. Take `session.Sessions` and
  `Acquire(ctx)` per request. See [chapter 12](12-scoped-instances.md#the-diagnostic-on-purpose).
- **`servo: X.ScopeKey must not name its receiver`** — write `func (*Session) ScopeKey(...)`.
  servo calls it on a typed nil, before any instance exists to call it on.
- **`servo: X declares a ScopeKey method but no servo.Scoped declares it`** — the method is there,
  the marker isn't. The diagnostic prints the exact `servo.Scoped[...]` line to add, along with the
  accessor interface to declare — unless your package already has one the generated accessor would
  satisfy, in which case it names that instead and asks only for the marker.
- **`servo: ScopeKey's key type is string, which is not a defined type`** — `type UserID string`,
  and return that. Scope identity is type identity.
- **`servo.ErrNoScopeKey` on every request** — the middleware isn't putting the key in the context,
  or is putting it in a context the handler doesn't see. `requireAuth` is the only place that
  should do it, and it must be on the `*http.Request` the handler receives.
- **`servo.ErrNoLifetime`** — `Acquire` was handed a `context.Background()` (or a `WithoutCancel`
  of one). Such a context can never be done, so the release backstop can never fire, so a forgotten
  `release()` would pin that instance forever. Pass the request's own context.
- **`servo.ErrScopeFull`** — more live keys than `servo.Max` allows. Raise the cap, shorten the
  linger window, or rate-limit whoever is generating keys.
- **A session's state disappears between two requests from the same user** — the linger window
  closed between them, or the key isn't stable. Check `Stats().Evictions` climbing faster than it
  should, and check that the key really is the same string for both requests.
- **`Flush` never runs** — it runs at eviction, not at the end of a request. With the tutorial's
  five-minute window that's five minutes after the user's last call, or at `Shutdown`, whichever
  comes first. `servotest.Linger(t, 0)` makes it immediate in a test.

## Messaging behaves unexpectedly

- **`notifier` never logs anything even though publishing reports success** — check that both
  sides use `broker.OrderPlacedSubject` rather than a hardcoded string anywhere; that's what turns
  a typo into a compile error instead of a silent mismatch. See [chapter
  7](07-messaging-layer.md).

## Tests fail in confusing ways

- **`missing call(s) to *MockX.Y`** — an `.EXPECT()` was set up but never invoked; either the code
  path didn't run, or the expectation is on the wrong mock. See [chapter 8](08-service-layer.md).
- **A test's mock calls seem to need a specific order, and sometimes fail** — gomock doesn't
  enforce order by default; use `gomock.InOrder(...)` if the order is actually load-bearing. See
  [chapter 8](08-service-layer.md).
- **A test fails on its second HTTP call, never its first, only after an unrelated change** — a
  `&resilience.Config{}` literal in the test is missing `RPS`. A struct literal skips
  `caarlos0/env` entirely, so the zero value (which clamps the rate limiter's burst to 1) applies
  instead of the configured default. See [chapter 14](14-resilience.md) and [chapter
  15](15-testing-strategy.md).
- **`t.Setenv(k, "")` doesn't produce the "required environment variable" error you expected** — an
  empty string still counts as a value for `,required` purposes. Use `os.Unsetenv` instead. See
  [chapter 15](15-testing-strategy.md).
- **A `NewTestApp`-based test returns an unexpected `500` instead of an obvious failure** — a
  `PanicReporter` panic raised *inside* a request handler is still caught by `recoverMiddleware` and
  turned into an ordinary `500`; check the logs for `"msg":"api: panic recovered"`. See [chapter
  11](11-wiring-with-servo.md).
- **A `NewTestApp`-based test crashes the whole process with a stack trace mentioning
  `servotest.PanicReporter`** — the same kind of panic, but firing *outside* any request (typically
  during `t.Cleanup`'s `ctrl.Finish()`), so nothing catches it. The panic message names the exact
  mock and method. See [chapter 11](11-wiring-with-servo.md).

## Tests pass when they shouldn't

- **`postgres`/`redis`/`natsbroker` tests report `ok`, but nobody's sure they ran anything real** —
  they skip via `t.Skip` when their `TEST_*` environment variable is unset, and a skip still reports
  `ok`. Confirm the variable is actually set — locally via `make up` plus `make test-integration`,
  in CI via the `services:` block. See [chapter 15](15-testing-strategy.md) and [chapter
  16](16-cicd.md).

## Observability isn't showing what's expected

- **No traces show up in Jaeger despite `OTLPEndpoint` being set correctly** — the SDK batches
  spans and exports on an interval, not immediately; give it several seconds, and double-check
  you're pointed at Jaeger's OTLP port (`4318`), not its UI port (`16686`). See [chapter
  13](13-observability.md).
- **`/metrics` shows a metric with far more label values than expected** — a label built from
  something request-specific (a raw path, an ID) instead of a bounded set. `route` here is safe
  because `r.Pattern` only ever takes one of a handful of registered values. See [chapter
  13](13-observability.md).
- **Log lines are plain text instead of JSON, right at process startup** — anything logged before
  `ConfigureLogging` runs uses the unconfigured default handler. See [chapter
  13](13-observability.md).

## Resilience mechanisms misbehave

- **A request hangs instead of failing fast when a dependency is down** — check the circuit
  breaker's `ReadyToTrip` is actually reachable; a custom `IsSuccessful`/`IsExcluded` can
  accidentally classify every real failure as a non-failure. See [chapter 14](14-resilience.md).
- **The circuit breaker "flaps"** (rapidly opens and closes) — `ReadyToTrip`'s threshold is
  probably tuned tighter than the dependency's real, normal error rate. See [chapter
  14](14-resilience.md).

## CI is red for a reason that isn't a real application bug

- **The `lint` job fails immediately with a wall of `errcheck` findings** on things like `defer
  conn.Drain()` or `tx.Rollback(ctx)` — these are idiomatic-to-ignore cleanup calls;
  `examples/tutorial/.golangci.yml` excludes exactly this set. If you see this on a *fresh* call
  site not already in that file, decide whether it's genuinely another safe-to-ignore case or an
  error your code should actually be handling before excluding it too. See [chapter
  16](16-cicd.md).
- **The workflow doesn't trigger on a PR that clearly touches `examples/tutorial/`** — check the
  `paths:` filter against the actual changed files. See [chapter 16](16-cicd.md).
- **`integration-test` fails immediately on every single test, not intermittently** — a `ports:`
  mapping and a test's `TEST_*` variable disagreeing on the port number, not a real connectivity
  problem. See [chapter 16](16-cicd.md).
- **A service container's health check never passes, and the job times out** — confirm the
  `--health-cmd` binary actually exists in that exact image tag; a slimmer or different image might
  not ship it. See [chapter 16](16-cicd.md).
- **`servo check` fails only in CI, never locally** — someone hand-edited `servo_gen.go` after
  generating it, and committed both. Regenerate; don't adjust the check. See [chapter
  16](16-cicd.md).
- **`docker-build` fails with a missing-module error `go build` doesn't reproduce locally** — the
  CI job's build context is already the repository root, so this isn't the wrong-directory problem
  a manual `docker build` can hit (see "Docker and deployment" below for that one). Check whether
  `.dockerignore` (or its absence) is excluding something the multi-stage build's `COPY . .` needs.
  See [chapter 16](16-cicd.md).

## Docker and deployment

- **`docker exec -it <container> sh` fails with `executable file not found`** — this is
  `distroless/static`'s entire point: no shell shipped. Debug via `docker compose logs` and `curl`
  instead, or temporarily swap the final `FROM` to a `debian:12-slim` base — never as something
  that ships. See [chapter 17](17-running-and-deployment.md).
- **`docker build -f examples/tutorial/deploy/Dockerfile .` fails with a missing-module error** —
  unlike the CI entry above, this is almost always the build context itself: run it from the
  repository root, not from inside `examples/tutorial/`. See [chapter
  17](17-running-and-deployment.md).

## Next

[Chapter 19: Alternatives and further reading](19-alternatives-and-further-reading.md) — the
choices this tutorial made at every layer, what the real alternatives were, and when you'd actually
want them instead.
