# 19. Alternatives and further reading

Every earlier chapter picked one option at each layer and ran with it, because a tutorial that
paused to weigh every alternative in depth at the point it arose would never finish building
anything. This chapter is where those weighed-but-deferred choices get their due — organized by
topic, meant to be read selectively (or searched, the way [chapter 18](18-troubleshooting.md) is)
rather than start to finish. Chapters 15, 16, and 17 already have their own `## Alternatives`
sections covering testing, CI, and deployment specifically; this chapter doesn't repeat those, only
links to them at the end.

## Dependency injection: servo and its alternatives

This whole tutorial is an argument for build-time DI via code generation, so it's worth being
explicit about what that's an argument *against*. [Google Wire](https://github.com/google/wire) is
the closest comparison — also build-time, also code-generated — but wires by writing explicit
provider sets rather than servo's structural capability detection; you trade servo's "add a
constructor, it's found automatically" ergonomics for Wire's fully explicit, nothing-inferred
graphs, which some teams prefer specifically because there's less to learn about *how* resolution
works. [Uber's Fx](https://github.com/uber-go/fx) and [Dig](https://github.com/uber-go/dig) go the
other direction — runtime reflection-based DI, no code generation step at all — trading servo's
compile-time "the graph either resolves or `servo generate` fails" guarantee for faster iteration
(no generation step to re-run) and runtime flexibility (conditional registration based on config).
And plenty of real, successful Go services use no DI framework at all: a single hand-written `New`
function in `main` that calls every constructor in dependency order. That's not a strawman —
`cmd/orders/main.go`'s generated `New` *is* essentially that function, just generated instead of
hand-maintained. The case for generating it is entirely about what happens as the graph grows: by
hand, a new dependency means finding every call site that needs it threaded through; generated,
it means adding a constructor parameter and re-running `servo generate`.

## HTTP routers

`api/server.go` uses Go 1.22+'s stdlib `http.ServeMux`, which chapter 10 covers in full. The
biggest name in the space, [gin-gonic/gin](https://github.com/gin-gonic/gin), buys back things the
stdlib router doesn't have — built-in request binding and validation, a middleware ecosystem,
generally faster routing for very large route tables — at the cost of a framework-specific handler
signature (`func(*gin.Context)` instead of `func(http.ResponseWriter, *http.Request)`) that makes
every handler harder to test without pulling gin into the test too.
[go-chi/chi](https://github.com/go-chi/chi) sits in between: it keeps the stdlib
`http.Handler`/`http.HandlerFunc` signatures exactly, and mostly adds route-grouping and middleware
composition sugar on top — a genuinely low-cost upgrade once a route table gets large enough that
`server.go`'s flat list of `mux.HandleFunc` calls stops being the most readable option. Five routes,
as this service has, is comfortably below that threshold; a few dozen, spread across several
resource types with shared middleware per group, might not be.

## Databases and query layers

Postgres via `pgx` (chapter 5) is the least controversial choice here — the most common real
answer for relational data in Go. Two axes are worth separating: which *database*, and which
*query layer* on top of it. On the database axis, MySQL is a reasonable, comparably-supported
alternative wherever organizational familiarity or existing infrastructure points that way; SQLite
(via `mattn/go-sqlite3` or the pure-Go `modernc.org/sqlite`) is worth knowing about specifically for
single-node deployments or embedded use cases, not as a production Postgres replacement at any real
scale. On the query-layer axis, `postgres/postgres.go` writes SQL directly against `pgx`'s
connection pool — no ORM, no query builder. [GORM](https://gorm.io/) and
[ent](https://entgo.io/) both trade that directness for generated or reflection-based query
construction, schema migrations tied to Go struct definitions, and (for ent specifically) a real
graph-aware query API for deeply relational data. The trade is real in both directions: hand-written
SQL means every query's actual behavior is visible in the file that runs it, at the cost of writing
more of it by hand; an ORM means less boilerplate for common cases, at the cost of a layer between
you and the SQL that's actually running — worth it once a schema has real complexity (many
relations, frequently-changing shapes), not obviously worth it for the two tables this service has.

## Migrations

`migrations/migrations.go` hand-rolls an embedded-SQL runner tracked via a `schema_migrations`
table — deliberately minimal, to keep the dependency count down for a tutorial. A real project
reaching for more maturity here has two well-established options:
[golang-migrate/migrate](https://github.com/golang-migrate/migrate) is the most widely used
standalone tool — versioned up/down migrations, a CLI, and library bindings for use exactly the way
this tutorial uses its own hand-rolled runner. [Atlas](https://atlasgo.io/) takes a different,
declarative approach: you describe the schema you *want*, and it generates the migration to get
there, rather than you writing the migration by hand — worth it once schema drift between
environments becomes a real, recurring problem rather than a hypothetical one.

## Caching

`redis/redis.go` implements cache-aside (chapter 6): read through the cache, fall back to the
repository on a miss, write back on write. That pattern generalizes past this tutorial's needs in
two directions worth naming. First, at higher read concurrency, a cache-aside implementation is
vulnerable to a *thundering herd* / *cache stampede*: many concurrent requests for the same missing
key all fall through to the repository simultaneously, right when the repository is least prepared
for a burst. `singleflight` (from `golang.org/x/sync`) is the standard fix — collapse concurrent
misses for the same key into one repository call, fan the result out to every waiter. This
tutorial's traffic never gets close to that threshold, which is exactly why it's absent here rather
than added speculatively. Second, an entirely different cache *shape* — write-through (write to the
cache and the store together, synchronously) — trades this tutorial's "cache might be briefly stale,
but a write never fails because of the cache" property (chapter 8's best-effort `Set`) for a
stronger consistency guarantee at the cost of every write now depending on the cache being up.

## Messaging: NATS, Kafka, and SQS

NATS was chosen for this tutorial specifically because it's simple to self-host via Docker with no
cloud account (chapter 7) — not because it's the only reasonable choice, or even the most common
one at scale. [Apache Kafka](https://kafka.apache.org/) is the heavyweight alternative: a durable,
partitioned commit log rather than NATS core's fire-and-forget pub/sub, with consumer groups,
offset-based replay, and genuinely enormous throughput headroom — at the cost of an operationally
heavier system (historically ZooKeeper, now KRaft-mode, but still meaningfully more to run and tune
than a single NATS binary) that's rarely justified until message volume or replay/audit
requirements actually demand it. AWS SQS (the broker this tutorial's predecessor project actually
used) trades self-hosting for a fully managed queue — no server to run at all — at the cost of
requiring an AWS account (or [LocalStack](https://www.localstack.cloud/) for local development,
which adds its own moving part) and AWS-specific semantics (visibility timeouts, at-least-once with
no native pub/sub fan-out the way NATS or Kafka have — SQS pairs with SNS for that). None of these
changes `broker.EventPublisher`'s shape; only `natsbroker/natsbroker.go`'s implementation would
need to change to swap the underlying system, which is exactly the point of that interface living
in its own package (chapter 7) rather than being called directly from `service`.

## The outbox pattern

`OrderService.CreateOrder` (chapter 8) publishes to NATS *after* the Postgres write commits, in the
same request, with a best-effort failure mode: if the publish fails, the order still exists, and
only a log line records the miss. That's an honest trade, not a hidden bug — it means an order can
exist without `notifier` ever having heard about it, if NATS is unreachable at exactly the wrong
moment. The **transactional outbox pattern** closes that gap: instead of publishing directly, write
the event to an `outbox` table in the *same* Postgres transaction as the order itself (so either
both commit or neither does), and have a separate poller (or Postgres's own logical replication, via
something like [Debezium](https://debezium.io/)) read unpublished outbox rows and publish them,
marking each as sent only after a confirmed publish. This buys guaranteed at-least-once delivery
tied to the same transaction as the write that caused it, at the cost of a new table, a poller
process (another `Runner`, in servo's terms), and the delivery now being asynchronous rather than
"probably already delivered by the time the HTTP response returns." Worth adding the moment a
missed `OrderPlaced` event is a real business problem rather than a notification-log inconvenience;
overkill before that.

## JWT signing algorithms

`auth/auth.go` signs with HS256 — a single shared secret both issues and verifies tokens (chapter
9). That's the right choice as long as exactly one service ever needs to verify a token, which is
true here. The moment a *second* service needs to verify tokens this one issues — an internal
"orders-read" service, say — HS256 means giving that second service the same secret this one uses
to *issue* tokens, which is more trust than "can verify" should require: any service holding an
HS256 secret can also forge tokens with it. **RS256** (or ES256) splits issuing from verifying: the
issuer holds a private key, and any number of verifiers only need the corresponding *public* key,
which can't be used to forge a token. The switch is mechanical in `golang-jwt/jwt/v5` — swap
`jwt.SigningMethodHS256` for `jwt.SigningMethodRS256`, sign with an `*rsa.PrivateKey` instead of a
`[]byte` secret, verify with the matching `*rsa.PublicKey` — but it adds real operational surface: a
key pair to generate, rotate, and (for verifiers outside this codebase entirely) a way to publish
the public key, typically a [JWKS](https://datatracker.ietf.org/doc/html/rfc7517) endpoint.

## Authentication beyond JWT

This tutorial issues its own tokens against its own user table. A real system integrating with an
existing identity provider — Okta, Auth0, a corporate SSO — would instead *verify* tokens issued by
that provider (via OIDC discovery and its published JWKS) rather than running `auth.Issuer.Issue`
at all, and `service.AuthService.Login` would shrink to "redirect to the identity provider," not
"check a password hash." Session-based auth (a server-side session store, an opaque cookie) is the
other classical alternative to JWTs generally — it trades JWT's "stateless, no lookup needed to
verify" property for the ability to instantly revoke a session. A JWT, once issued, is valid until
it expires; the only way to revoke one early is an additional mechanism this tutorial doesn't have
— a blocklist of revoked token IDs, checked on every request, which reintroduces exactly the
per-request lookup JWTs were meant to avoid. A session store pays that cost upfront and
consistently; JWTs avoid it until the day something needs to be revoked early.

## REST vs gRPC

`api/` speaks JSON over HTTP because that's the lowest-friction choice for a public-facing API a
browser or `curl` can talk to directly, with no code generation step required to consume it — the
right default for a service like this one, whose primary caller is human-driven tooling and simple
clients. [gRPC](https://grpc.io/) is the standard alternative once the primary caller is *other
services*, not humans: Protocol Buffers give you a strongly-typed contract with generated client and
server code in any supported language, HTTP/2 multiplexing, and built-in streaming (server-side,
client-side, or bidirectional) that REST has no equivalent for without falling back to
WebSockets or SSE. The cost is real too: a `.proto` file and a generation step between changing a
message shape and using it, less human-readable wire format (not something you `curl` and read
directly), and a steeper on-ramp for a consumer that just wants to try the API from a browser tab.
Plenty of real systems use both — gRPC internally between services, REST (or GraphQL) at the public
edge — which is a reasonable default to reach for once "internal" and "public" API consumers
actually diverge.

## Structured logging libraries

`observability/logging.go` uses `log/slog`, the stdlib structured logger added in Go 1.21 —
covered fully in chapter 13. Before `slog` existed, [Uber's
zap](https://github.com/uber-go/zap) and [zerolog](https://github.com/rs/zerolog) were the two
dominant third-party answers to the same problem (structured, leveled, reasonably fast logging),
and both are still reasonable choices today — zap in particular still edges out `slog` on raw
allocation-per-log-call benchmarks in the highest-throughput services, where that specific
difference is actually load-bearing. For everything short of that, `slog` being in the standard
library (no dependency, guaranteed long-term stability, integrates with anything else that accepts
an `slog.Handler`) is a hard default to argue against now that it exists.

## Metrics and tracing vendors

Prometheus (chapter 13) and OpenTelemetry are both vendor-neutral by design, which is precisely why
they were chosen — swapping *where* metrics and traces end up shouldn't mean rewriting
instrumentation. On the metrics side, the real alternatives are less about competing client
libraries and more about where scraped (or pushed) data ends up: Prometheus itself, a managed
equivalent (Grafana Cloud, Datadog's own agent, AWS Managed Prometheus), or a push-based system like
StatsD where the application sends metrics out rather than waiting to be scraped. On the tracing
side, this tutorial exports to Jaeger over OTLP specifically because it's simple to self-host; the
same `otlptracehttp` exporter this codebase already uses would send the exact same spans to any
other self-hosted, TLS-optional OTLP collector — Grafana Tempo, for instance — by changing
`OTLPEndpoint` alone. A hosted vendor like Honeycomb or Datadog needs a bit more:
`observability.NewTracer` currently calls `otlptracehttp.WithInsecure()` unconditionally, and
no package declares a field for an API-key header, both of which a real hosted backend requires
(TLS, plus per-vendor authentication). Neither is a deep change — `otlptracehttp.WithHeaders(...)`
and making `WithInsecure()` conditional are both small, additive edits — but it's not the "change
one string, done" story a self-hosted collector gets. That portability gap being small and
mechanical, rather than a rewrite, is still the argument for instrumenting against OpenTelemetry's
API instead of a vendor-specific SDK directly.

## Per-client rate limiting

`resilience.RateLimiter` (chapter 14) is one shared token bucket for the entire process — every
request, from every client, draws from the same budget. That protects the process itself from being
overwhelmed, but it means one noisy or abusive client can consume the whole budget and cause
`429`s for every other client too. **Per-client rate limiting** — a separate bucket keyed by API
key, authenticated user ID, or source IP — fixes that at the cost of real bookkeeping: a map (or an
external store like Redis, if limits need to survive a restart or be shared across replicas) from
key to bucket, and a decision about what happens when that map grows unbounded with one-off or
malicious keys (an LRU eviction policy, typically). `golang.org/x/time/rate` — the same package this
tutorial already depends on — supports exactly this shape too, via one `*rate.Limiter` per key
instead of one for the whole process; the change is more about state management than about needing
a different library. Worth adding the moment a single tenant hogging the whole service's budget is
a realistic complaint, not before.

That per-client vs. global distinction matters even more once a service runs as more than one
replica, which is the next section's subject.

## Per-user state without a scope

`session.Session` (chapter 12) is one instance per user, generated by servo. Three other shapes
solve the same problem, and each is better in a case this one isn't.

**Redis, or any shared store.** Put the recently-viewed list behind the cache you already have,
keyed by user ID. Survives a restart, shared across replicas, no in-memory anything — which is the
right answer the moment the state actually matters, or the moment there is more than one replica
and sticky routing isn't on the table. What you give up is speed and simplicity: every read is a
network round trip, and you now serialize something that was a slice of UUIDs.

**A plain `map[UserID]*Session` beside servo.** Perfectly reasonable, and what a scope is
generating for you. The work it doesn't save you from is what a scope's fifty-line state machine is
actually for: the reference count, the eviction that has to be atomic with reaching zero, the
lookup-then-use race, the linger window that stops the instance being rebuilt per request, and the
teardown ordering. Write it if you want it; just don't assume it's ten lines. And nothing will tell
you when a singleton captures one of its values.

**No shared instance at all — recompute per request.** If the state is cheap to derive, this is
strictly simpler, and worth checking before reaching for either of the above. A scope earns its
keep when there is something genuinely worth keeping between two requests.

The one shape that doesn't apply here: **distinct types**. Two SQS accounts, a primary and a
replica database, an A/B pair — those are a closed, known set, and belong in the previous model
(two named types, two graph nodes). A scope is for an open key space that comes from outside.

## Kubernetes and beyond docker-compose

`docker-compose.yml` (chapter 17) is explicitly a local-development convenience — one host, one
instance of each container, torn down and recreated freely. A production Kubernetes deployment
changes several things this tutorial's local setup doesn't have to consider:

- **`orders` becomes a `Deployment`** with `replicas > 1` for availability. Postgres typically
  becomes either a managed external database (RDS, Cloud SQL) or, if self-hosted in-cluster, a
  `StatefulSet` with a persistent volume claim — `docker-compose.yml`'s Postgres has no volume at
  all, and loses all data on `make down` deliberately, which is fine locally and never acceptable
  in production.
- **`/healthz` and `/readyz` become `livenessProbe` and `readinessProbe`** on the `Deployment`
  spec, polling the exact same admin-port endpoints chapter 10 and 13 already built — no new code,
  just a Kubernetes manifest pointing at what already exists.
- **`JWT_SECRET` becomes a `Secret`**, mounted as an environment variable via `secretKeyRef` rather
  than written into a manifest or a compose file in plain text (chapter 17's own do's and don'ts
  flags this exact gap in the tutorial's local setup).
- **The rate limiter's meaning changes with `replicas`.** `resilience.RateLimiter`'s shared,
  in-process token bucket (see "Per-client rate limiting" above) means the *effective* service-wide
  limit becomes `RateLimitRPS × replicas`, not `RPS` — three replicas at the default `50`
  really allow `150` requests per second in aggregate, each replica independently enforcing its own
  slice. That may be exactly what you want (limit protects each *instance*, and you scale replicas
  to scale the aggregate limit) or may not be (you actually wanted one global ceiling) — worth
  deciding deliberately rather than discovering under load. A genuinely shared limit across
  replicas needs external state (Redis-backed rate limiting, or a limit enforced at an API gateway
  in front of every replica) instead of this tutorial's in-process bucket.
- **A `HorizontalPodAutoscaler`** watching CPU or the `/metrics` endpoint's request rate can add and
  remove replicas automatically — interacting with the point above the same way manually-set
  replicas would.

None of this requires touching `examples/tutorial`'s Go code; it's entirely a deployment-manifest
concern layered on top of the same binary and the same `/healthz`/`/readyz`/`/metrics` contract
chapters 10 and 13 already built for exactly this reason.

## Order lifecycle as a state machine

`domain.OrderStatusPending` is the only status this tutorial ever assigns — the OpenAPI spec says so
directly in its own schema description. A real order-management system needs more:
`pending → paid → shipped → delivered`, with `cancelled` or `refunded` branching off at various
points, and — critically — rules about which transitions are even legal from a given state
(a `delivered` order shouldn't be cancellable the same way a `pending` one is). That's a state
machine, not just a wider enum: `domain.Order` would gain a method like `CanTransitionTo(next
OrderStatus) bool` (or an equivalent table of legal transitions), and `OrderService` would check it
before any status-changing write, returning `domain.ErrValidation` (chapter 4) for an illegal
transition the same way it already does for a non-positive quantity. Left out here because the
single `pending` status this tutorial needs doesn't exercise it — adding the machinery without a
second status to transition to or from would be exactly the "designing for a hypothetical future
requirement" this codebase otherwise avoids.

## Idempotency keys

`POST /orders` as built has no protection against a client's retried request (a timeout where the
first attempt actually succeeded, a double-click, a naive client-side retry loop) creating two
identical orders. An **idempotency key** — a client-generated unique value sent in a header
(`Idempotency-Key`, by convention) — fixes this: the server records which keys it's already
processed and, on a repeat, returns the *original* response instead of creating a second order.
Implementing it needs a place to store the key-to-response mapping (a new table, or this service's
existing Redis, with a bounded TTL — keys don't need to be remembered forever) and a middleware or
service-layer check ahead of `CreateOrder` itself. Genuinely important for a real payments-adjacent
API; left out here because demonstrating cache-aside reads (chapter 6) already covers the
"talk to Redis" mechanics this would otherwise duplicate, and adding it without a caller that
actually retries would be more speculative plumbing than a real requirement.

## API versioning

Every response shape in this tutorial (`orderResponse`, `loginResponse`, and the rest) is
implicitly "version 1" — there's no version anywhere in a URL, a header, or the DTOs themselves.
Three real strategies exist for when a breaking change is needed: **URL versioning**
(`/v2/orders`, a new route registered alongside the old one — simple, visible, but means every
resource needs its own `/v2` the moment any one of them changes), **header versioning** (an
`Accept: application/vnd.servoorders.v2+json` media type, or a custom version header — keeps URLs
stable, but is invisible in a browser address bar and easy for a client to get wrong silently), and
**field-additive evolution** (never remove or repurpose a field, only add new optional ones —
avoids versioning entirely for a surprisingly large fraction of real changes, but doesn't help
when a field's *meaning* needs to change, not just its presence). Left unaddressed here because
this API has shipped exactly one shape, and premature versioning of an API with no real second
version yet is its own kind of speculative complexity.

## Testing framework alternatives

Chapter 15 already covers table-driven-vs-separate-function tests, testcontainers-go, and contract
testing as alternatives to the specific choices `examples/tutorial`'s test suite made — see that
chapter's own `## Alternatives` section. One more, chapter-1-table-level choice worth naming here:
this suite uses plain stdlib `if got != want { t.Errorf(...) }` assertions, not a matcher library.
[testify](https://github.com/stretchr/testify)'s `assert`/`require` packages are the most common
alternative — `assert.Equal(t, want, got)` instead of a hand-written comparison and message, with
richer diff output on failure for complex structs. [ginkgo](https://github.com/onsi/ginkgo) and its
matcher library `gomega` go further, adding an RSpec-style BDD structure (`Describe`/`Context`/`It`)
on top. Both are genuine quality-of-life improvements once assertions get repetitive or structurally
nested enough that hand-written comparisons stop being the most readable option — this tutorial's
comparisons never got there, which is a statement about this codebase's current size, not a verdict
on the libraries.

## See also

- [Chapter 12](12-scoped-instances.md): Redis, a hand-written registry, or recomputing per request.
- [Chapter 15](15-testing-strategy.md#alternatives): testcontainers-go, table-driven tests, contract testing.
- [Chapter 16](16-cicd.md#alternatives): combined vs. split CI jobs, `actions/cache`, other CI systems, Dependabot.
- [Chapter 17](17-running-and-deployment.md#alternatives): image registries, plain `docker run` vs. Compose, debug sidecars.

## Further reading

- [*Designing Data-Intensive Applications*](https://dataintensive.net/) (Kleppmann) — the deepest
  available treatment of the consistency, replication, and delivery-guarantee questions underneath
  "cache-aside," "at-least-once," and "the outbox pattern" above.
- [The Twelve-Factor App](https://12factor.net/) — the source of the configuration philosophy
  chapter 3 follows (config from the environment, no in-code defaults for anything environment-
  specific).
- [OpenTelemetry documentation](https://opentelemetry.io/docs/) — this tutorial's tracing setup
  (chapter 13) uses a small slice of a much larger spec, including metrics and logs signals this
  codebase still sends via Prometheus and `slog` directly rather than through OTel.
- [The Go Blog: JSON and Go](https://go.dev/blog/json) and [Go 1.22's routing
  enhancements](https://go.dev/blog/routing-enhancements) — background on two stdlib capabilities
  (`encoding/json`, `http.ServeMux` pattern matching) this tutorial leans on directly.

## Closing

That's the whole tutorial: a layered service, wired by servo, backed by real Postgres, Redis, and
NATS, authenticated, observable, resilient to its dependencies failing, tested at four different
levels, built and deployed by CI, documented with a real OpenAPI contract. If you want to keep
going rather than stop here, the sections above point at concrete next features — an idempotency
key, a real order-status state machine, per-client rate limiting — that would each extend the real
codebase in `examples/tutorial/` rather than a hypothetical one, using exactly the layers and
patterns the last eighteen chapters already established. Go build the next thing on top of it.
