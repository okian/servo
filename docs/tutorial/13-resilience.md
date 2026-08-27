# 13. Resilience

[Chapter 6](06-caching-layer.md) already made the service layer tolerate a broken cache: any
`Get` error other than `cache.ErrMiss` gets logged and treated as a fallback to Postgres. That's
correct, but slow under real failure — every single request pays the cost of a doomed call to
Redis (a real network round trip, waiting for a connection timeout) before falling back. A circuit
breaker fixes the *speed* of an already-correct fallback: after enough consecutive failures, stop
even trying, until enough time has passed to check again. This chapter adds that, plus a rate
limiter protecting the service from being overwhelmed by its own callers.

## Wrap the cache, without wrapping the interface it also implements

Create `resilience/breaker.go`. The type it wraps is worth choosing carefully, so start there:

```go
package resilience

import (
	"context"
	"errors"
	"fmt"

	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
	"example.com/servoorders/redis"
	"github.com/google/uuid"
	"github.com/sony/gobreaker/v2"
)

type CircuitBreakerCache struct {
	// Stored as the interface, not *redis.Cache, purely so this package's
	// own tests can substitute a mock by constructing this struct directly
	// (whitebox — see breaker_test.go).
	next    cache.OrderCache
	breaker *gobreaker.CircuitBreaker[any]
}

func NewCircuitBreakerCache(next *redis.Cache) *CircuitBreakerCache {
	return &CircuitBreakerCache{
		next: next,
		breaker: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
			Name: "cache",
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures > 5
			},
		}),
	}
}

var _ cache.OrderCache = (*CircuitBreakerCache)(nil)
```

The constructor parameter is `*redis.Cache` — the concrete type — not `cache.OrderCache`, even
though the field that stores it is the interface. This isn't a style inconsistency; it's the one
choice that keeps the graph resolvable at all. `CircuitBreakerCache` is about to become the thing
`cache.OrderCache` resolves to (via `servo.Bind` below). If its own constructor also asked for
`cache.OrderCache`, servo would have to resolve that parameter — and the binding would send it
right back to `CircuitBreakerCache` itself, a cycle with no real dependency underneath it. Asking
for the concrete type it actually wraps breaks that, at the cost of `CircuitBreakerCache` only ever
being able to wrap `*redis.Cache` specifically, not some future second cache implementation. For
one implementation, that's a fine trade.

Now `Get`, where an open breaker becomes something the service layer already knows how to handle:

```go
func (c *CircuitBreakerCache) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	order, err := c.breaker.Execute(func() (any, error) {
		return c.next.Get(ctx, id)
	})
	if errors.Is(err, gobreaker.ErrOpenState) {
		return nil, cache.ErrMiss
	}
	if err != nil {
		return nil, err
	}
	return order.(*domain.Order), nil
}
```

Mapping `gobreaker.ErrOpenState` to `cache.ErrMiss` — rather than a new, distinct error — means
[chapter 8](08-service-layer.md)'s `GetOrder` needs no changes at all. From its point of view,
"the cache is refusing to even try right now" and "this key isn't cached" call for the identical
response: fall back to the repository. `Set` and `Invalidate` round out the interface, with no new
ideas:

```go
func (c *CircuitBreakerCache) Set(ctx context.Context, o *domain.Order) error {
	_, err := c.breaker.Execute(func() (any, error) {
		return nil, c.next.Set(ctx, o)
	})
	if err != nil {
		return fmt.Errorf("resilience: %w", err)
	}
	return nil
}

func (c *CircuitBreakerCache) Invalidate(ctx context.Context, id uuid.UUID) error {
	_, err := c.breaker.Execute(func() (any, error) {
		return nil, c.next.Invalidate(ctx, id)
	})
	if err != nil {
		return fmt.Errorf("resilience: %w", err)
	}
	return nil
}
```

## Prove the breaker actually opens

A live Redis is the wrong tool for testing this precisely — you'd need to reliably force six
consecutive real failures on demand. Instead, construct the struct directly with a mock in its
`next` field, bypassing the exported constructor's `*redis.Cache` requirement entirely (only
possible because this test file is `package resilience`, not `resilience_test`):

```go
func newTestBreakerCache(t *testing.T, tripAfter uint32) (*CircuitBreakerCache, *mocks.MockOrderCache) {
	t.Helper()
	mock := mocks.NewMockOrderCache(gomock.NewController(t))
	return &CircuitBreakerCache{
		next: mock,
		breaker: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
			Name: "test",
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= tripAfter
			},
		}),
	}, mock
}
```

```go
func TestCircuitBreakerCacheOpensAfterConsecutiveFailuresAndReportsAMiss(t *testing.T) {
	bc, mock := newTestBreakerCache(t, 2)
	failure := errors.New("redis: connection refused")
	id := uuid.New()

	// Two real failures trip the breaker (ReadyToTrip fires on the second).
	mock.EXPECT().Get(gomock.Any(), id).Return(nil, failure).Times(2)
	for range 2 {
		if _, err := bc.Get(context.Background(), id); !errors.Is(err, failure) {
			t.Fatalf("Get before the breaker trips: err = %v, want %v", err, failure)
		}
	}

	// The breaker is now open: no third call should ever reach the mock —
	// there is deliberately no third .EXPECT() above, so gomock itself
	// fails the test if Get calls through anyway.
	got, err := bc.Get(context.Background(), id)
	if !errors.Is(err, cache.ErrMiss) {
		t.Errorf("Get with an open breaker: err = %v, want cache.ErrMiss", err)
	}
	if got != nil {
		t.Errorf("Get with an open breaker: got = %v, want nil", got)
	}
}
```

Using a small, explicit `tripAfter` (2, not the real code's 5) is what makes this deterministic:
the test proves the *mechanism* — real failures happen and are surfaced normally, then the breaker
opens and stops calling through at all, reporting a miss instead — without needing to wait out
whatever threshold production actually uses.

```
$ go test ./resilience/... -v
=== RUN   TestCircuitBreakerCachePassesThroughWhenClosed
--- PASS: TestCircuitBreakerCachePassesThroughWhenClosed (0.00s)
=== RUN   TestCircuitBreakerCacheOpensAfterConsecutiveFailuresAndReportsAMiss
--- PASS: TestCircuitBreakerCacheOpensAfterConsecutiveFailuresAndReportsAMiss (0.00s)
=== RUN   TestRateLimiterAllowsRequestsWithinTheLimit
--- PASS: TestRateLimiterAllowsRequestsWithinTheLimit (0.00s)
=== RUN   TestRateLimiterRejectsRequestsOverTheLimit
--- PASS: TestRateLimiterRejectsRequestsOverTheLimit (0.00s)
PASS
ok  	example.com/servoorders/resilience	0.312s
```

## See it against a real Redis

The unit test above is the precise proof; running the real thing is the sanity check that it
actually behaves the same way outside a test. Start everything, log in, create an order, then stop
Redis mid-session:

```
$ make up
$ POSTGRES_DSN=... REDIS_ADDR=... NATS_URL=... JWT_SECRET=... go run ./cmd/orders
```
```
$ docker stop <redis container>
$ curl -s -w " [%{http_code}]\n" http://localhost:8080/orders/<id> -H "Authorization: Bearer $TOKEN"
{"id":"...","item":"widget",...}
 [200]
```

The request still succeeds — Postgres answers it. The first several attempts after stopping Redis
each cost a real, logged connection failure:

```
{"level":"ERROR","msg":"service: cache read failed, falling back to repository","error":"redis: get: dial tcp [::1]:6379: connect: connection refused"}
```

Keep making requests and those log lines stop appearing well before Redis comes back — the breaker
has opened, and every subsequent attempt is now a `cache.ErrMiss` returned immediately, with no
network call and nothing to log. `/healthz` still correctly reports `redis.Cache` as `"failed"`
the whole time: the breaker and the health check are deliberately independent signals. The breaker
answers "should I bother trying," measured across the cache's own calls; `Health` answers "is this
dependency actually up right now," checked directly. A cache that's down should show up as
unhealthy regardless of how well the breaker is protecting the request path from it.

## Bind it in, ahead of the raw cache

In `cmd/orders/spec.go`, the service layer needs to receive the wrapped cache, not `redis.Cache`
directly:

```go
servo.Bind[repository.OrderRepository, *postgres.Store](),
servo.Bind[repository.UserRepository, *postgres.Store](),
servo.Bind[broker.EventPublisher, *natsbroker.Publisher](),

// The service layer gets the circuit-breaker-wrapped cache, not
// redis.Cache directly.
servo.Bind[cache.OrderCache, *resilience.CircuitBreakerCache](),
```

`redis.Cache` itself is still constructed and still gets its `Init`/`Stop`/`Health` capabilities
called exactly as before — it just arrives at `service.OrderService` through
`CircuitBreakerCache` now instead of directly.

## Rate limiting

Create `resilience/ratelimit.go`:

```go
package resilience

import (
	"net/http"

	"example.com/servoorders/config"
	"golang.org/x/time/rate"
)

type RateLimiter struct {
	limiter *rate.Limiter
}

func NewRateLimiter(cfg *config.Config) *RateLimiter {
	burst := max(int(cfg.RateLimitRPS), 1)
	return &RateLimiter{limiter: rate.NewLimiter(rate.Limit(cfg.RateLimitRPS), burst)}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.limiter.Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

This is one shared bucket for the entire service, not one per client. That's the simplest thing
that actually protects the process — a burst of traffic from anywhere gets throttled — but it also
means one aggressive client can use up budget that a well-behaved client needed. A real
multi-tenant service usually wants a bucket per client, keyed by API key or IP, which trades this
simplicity for needing an eviction strategy so that map of limiters doesn't grow without bound;
see [chapter 18](18-alternatives-and-further-reading.md#per-client-rate-limiting).

## A wiring mistake worth walking through, not just avoiding

The obvious next step is wrapping the whole chain in one more layer: `recover(metrics(limiter(tracer(logging(mux)))))`,
so that a 429 the limiter rejects still shows up in `orders_http_requests_total` before falling
through. That compiles, every existing test still passes, and it's wrong — in a way unit tests
for `Metrics` and `RateLimiter` in isolation can't catch, because the bug only exists in how they
compose with `tracer.Middleware`.

Here's the actual mechanism. `tracer.Middleware` wraps `next` in `otelhttp.NewHandler`, which —
to attach a span to the request context — calls `r = r.WithContext(ctx)` before invoking anything
downstream. `(*http.Request).WithContext` doesn't mutate the original request; it allocates a new
one and copies the old fields into it. `http.ServeMux` (further downstream, inside `logging` and
`mux`) sets `r.Pattern` on *that* forked copy — not on whatever `*http.Request` an outer
middleware, like `Metrics.Middleware`, is still holding a reference to. Put `metrics` outside
`tracer`, and `route := r.Pattern` reads a `*http.Request` that was never the one `ServeMux`
actually touched. It's empty, every time, for every request — not just the rejected ones.

Running the real service (not just the unit tests) makes this obvious in about a minute:

```
$ curl -s http://localhost:8081/metrics | grep orders_http_requests_total
orders_http_requests_total{method="POST",route="unmatched",status="201"} 1
```

`status="201"` — the request succeeded. `route="unmatched"` — the label that was supposed to
be `"POST /orders"`, on every single request, not only the throttled ones.

There's a second, independent problem with the "just reorder it" instinct once you notice the
first one: `Metrics` has to sit *inside* `tracer` for the label to work, but `limiter` has to sit
*outside* `tracer` so a rejected request never starts a span. And a 429 rejected by `limiter`
never reaches whatever's inside it — so `Metrics` sitting inside `tracer` (which is inside
`limiter`) can *never* see a rejected request at all, correct label or not. Three real
requirements — correct label, rejections never traced, rejections still counted — and no single
linear order of three middlewares satisfies all three simultaneously.

The fix isn't a fourth reordering attempt; it's recognizing the third requirement doesn't actually
need `requests_total` at all. A rejected request has no route to report in the first place — it
never reached the mux that would have told you what route it was headed for. Give the limiter its
own counter instead of trying to route its rejections through a metric built for something else.
Go back to `observability/metrics.go` and add one method:

```go
// NewCounter registers a counter into this same registry, for any other
// component that wants its own metric on the same /metrics endpoint
// without either package needing a shared global.
func (m *Metrics) NewCounter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	m.registry.MustRegister(c)
	return c
}
```

And use it in `resilience/ratelimit.go`:

```go
type RateLimiter struct {
	limiter    *rate.Limiter
	rejections prometheus.Counter
}

func NewRateLimiter(cfg *config.Config, metrics *observability.Metrics) *RateLimiter {
	burst := max(int(cfg.RateLimitRPS), 1)
	return &RateLimiter{
		limiter:    rate.NewLimiter(rate.Limit(cfg.RateLimitRPS), burst),
		rejections: metrics.NewCounter("orders_rate_limit_rejections_total", "Total requests rejected by the rate limiter."),
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.limiter.Allow() {
			rl.rejections.Inc()
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

Now the three requirements stop conflicting: `metrics` stays directly against `logging`/`mux` (so
its label is always correct), `limiter` sits outside `tracer` (so a rejection never starts a
span), and rejections are counted by their own metric the moment they happen, regardless of where
`limiter` sits in the chain. This is also, independently, a better metric to have — a dashboard
answering "how often are we shedding load" shouldn't have to filter a general-purpose HTTP counter
for a synthetic route value to find out.

A test proves the counter actually moves:

```go
func TestRateLimiterRejectsRequestsOverTheLimitAndCountsIt(t *testing.T) {
	metrics := observability.NewMetrics()
	rl := resilience.NewRateLimiter(&config.Config{RateLimitRPS: 0}, metrics) // burst clamped to 1
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("second request status = %d, want 429", second.Code)
	}

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "orders_rate_limit_rejections_total 1") {
		t.Errorf("metrics output missing orders_rate_limit_rejections_total 1, got:\n%s", rec.Body.String())
	}
}
```

A bare `&config.Config{...}` literal is what makes `RateLimitRPS: 0` easy to reach for here on
purpose — it skips `caarlos0/env`'s tag processing entirely, so any field not set explicitly is
Go's zero value, not the configured `envDefault`. That's exactly why every *other* test in this
codebase that constructs a `&config.Config{}` literal needs `RateLimitRPS` set to something
generous (`api_test.go`'s fixtures use `1000`): forgetting it there isn't a deliberate test of the
limiter, it's a burst of one silently breaking the second HTTP call any other test happens to make.

## Wire it into api.Server

`New` gains one more parameter, and the middleware chain gets one more layer, in the order the
walkthrough above landed on:

```go
func New(
	cfg *config.Config,
	orders *service.OrderService,
	authSvc *service.AuthService,
	issuer *auth.Issuer,
	metrics *observability.Metrics,
	tracer *observability.Tracer,
	limiter *resilience.RateLimiter,
) *Server {
	s := &Server{orders: orders, auth: authSvc, metrics: metrics}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /orders", requireAuth(issuer, s.handleCreateOrder))
	mux.HandleFunc("GET /orders/{id}", requireAuth(issuer, s.handleGetOrder))
	mux.HandleFunc("GET /orders", requireAuth(issuer, s.handleListOrders))

	// Outermost first: recover must see a panic from anything below it.
	// Metrics has to sit directly against logging/mux, not further out —
	// otelhttp's handler (inside tracer.Middleware) forks the request via
	// r.WithContext before passing it on, and http.ServeMux sets r.Pattern
	// on that fork, not on whatever *http.Request an outer middleware is
	// still holding. limiter sits outside tracer so a rejected request
	// costs no span; it counts its own rejections directly rather than
	// trying to route them through requests_total, which a rejected
	// request never reaches anyway.
	handler := loggingMiddleware(mux)
	handler = metrics.Middleware(handler)
	handler = tracer.Middleware(handler)
	handler = limiter.Middleware(handler)
	handler = recoverMiddleware(handler)

	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: handler}
	return s
}
```

Regenerate (`servo generate`), and `go build` will tell you immediately if any constructor call —
your own code, or a test — still passes the old argument count. That's `servo_gen.go`'s own
comment header confirming the shape actually resolved:

```
//	[L2] *example.com/servoorders/redis.Cache
//	      deps: *example.com/servoorders/config.Config
//	      capabilities: Initializer, Finalizer, Healther | binding: sole candidate | redis/redis.go:30:6
//	[L3] *example.com/servoorders/resilience.CircuitBreakerCache
//	      deps: *example.com/servoorders/redis.Cache
//	      capabilities: none | binding: explicit bind | resilience/breaker.go:38:6
```

`redis.Cache` is still level 2, still has all three capabilities, still constructed and started
exactly as before — `CircuitBreakerCache` just sits one level above it now, with no capabilities
of its own, purely logic wrapping logic.

## Diagnostics

- **A request hangs instead of failing fast when the cache is down** — check the breaker's
  `ReadyToTrip` is actually reachable; if every "failure" is being classified as a success by a
  custom `IsSuccessful`, or excluded by `IsExcluded`, the breaker never opens no matter how badly
  the dependency is failing.
- **Tests fail intermittently on the second HTTP call in a test, never the first** — the
  `RateLimitRPS`-left-at-zero trap above. Check every `&config.Config{}` literal in the failing
  test sets it explicitly.
- **The circuit breaker "flaps"** (rapidly opens and closes) — usually means `ReadyToTrip`'s
  threshold is too sensitive for the dependency's real, normal error rate (a cache that legitimately
  times out 1% of the time under load shouldn't trip a breaker tuned for "any failure at all").
  Tune `ReadyToTrip` against the dependency's observed baseline, not a guess.

## Do's and don'ts

- **Do** make a circuit breaker's "open" state collapse into whatever error the caller already
  handles, when that's honest — `cache.ErrMiss` here is a real example, not a hack, because a
  failing cache and a missing cache entry genuinely call for the same response.
- **Do** keep `Health` reporting the dependency's real state independent of any breaker in front of
  it. Hiding a real outage behind "well, the breaker's handling it" is how a dependency being down
  for hours goes unnoticed until something worse fails too.
- **Don't** wrap everything in a circuit breaker reflexively. Postgres in this service has no
  breaker — a database this service treats as its primary source of truth failing isn't a
  "degrade gracefully" situation the way a cache miss is; there's nothing to fall back to.
- **Don't** size a global rate limiter around peak legitimate traffic without margin. `RateLimitRPS`
  protects the process from being overwhelmed; set too tight, it starts rejecting the traffic it
  was supposed to serve.

## Next

[Chapter 14: Testing strategy](14-testing-strategy.md) — pulling together the unit, integration,
and API-level tests written across every chapter so far into one coherent picture.
