# 13. Observability

The service works end to end now — including the one per-user piece
[chapter 12](12-scoped-instances.md) added — but from the outside, a running instance is a black
box: there's no way to tell whether it's healthy, slow, or actually doing what a request asked for
without attaching a debugger. This chapter adds the three tools that answer those questions in
production: structured logs for "what happened," metrics for "how much and how fast," and traces
for "what did this one request actually do."

## Logs first, and why they're not a servo component

Every log line so far has used `log/slog`'s unconfigured default logger — plain text, no level
filtering. Fixing that is one function, and it deliberately isn't wired through servo. Create
`observability/logging.go`:

```go
package observability

// Logger is a defined type rather than a bare *slog.Logger. That matters
// more than it looks: a foreign type comes with foreign providers.
// slog.Default and a transitive dependency's logr helper both return
// *slog.Logger, and servo has no basis for choosing between them — it
// reports the ambiguity rather than guessing. Owning the type is the same
// rule as owning your configuration (chapter 3).
//
// The embedded *slog.Logger means callers write log.InfoContext(...)
// exactly as they would have.
type Logger struct{ *slog.Logger }

type Config struct {
	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, "")
}

func NewLogger(cfg *Config) *Logger {
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(l)
	return &Logger{Logger: l}
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

`NewLogger` is an ordinary provider, and that is the whole point.

### Why the logger is a node, and not a call at the top of `main`

The tempting shape is a plain function called before anything else:

```go
// don't do this
observability.ConfigureLogging(cfg) // before anything else has a chance to log
app, err := New(ctx)
```

The argument for it is real: logging setup has to happen before anything logs, and calling it first
obviously achieves that. But look at what that sentence is doing — it is an *ordering assertion*,
made by a human, in a comment. It is exactly the kind of claim this whole tool exists to derive from
dependencies rather than assert, and servo cannot check it. Add a component tomorrow whose
constructor logs, and nothing tells you the ordering assumption still holds.

Making the logger a node removes the question instead of answering it. Anything that logs takes a
`*observability.Logger`:

```go
func New(cfg *Config, log *observability.Logger) *Notifier
func New(repo repository.OrderRepository, c cache.OrderCache, publisher broker.EventPublisher, log *observability.Logger) *OrderService
```

so the logger is constructed before them by the same rule that orders everything else. "Did logging
get configured before X" is not a question anyone can ask, because X cannot be built without it.
`main` configures nothing at all now:

```go
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := New(ctx)
	if err != nil {
		log.Fatal(err)
	}
	...
}
```

`NewLogger` still calls `slog.SetDefault`, and that is not a leftover. It is for code servo does not
wire: the standard library and third-party packages log through `slog.Default()` and have no
constructor to inject into. Our own code never relies on it — every component here takes the logger.
The global became a *consequence* of building the logger rather than the mechanism for configuring
it, and it now happens at the right moment by construction.

## Metrics, and a cardinality trap worth knowing about

Create `observability/metrics.go`:

```go
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry        *prometheus.Registry
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "orders_http_requests_total",
			Help: "Total HTTP requests, by method, route, and status.",
		}, []string{"method", "route", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "orders_http_request_duration_seconds",
			Help: "HTTP request duration in seconds, by method and route.",
		}, []string{"method", "route"}),
	}
	m.registry.MustRegister(m.requestsTotal, m.requestDuration)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
```

`NewMetrics` builds its own `*prometheus.Registry` rather than registering into
`prometheus.DefaultRegisterer` — deliberately. Prometheus panics on a duplicate metric
registration, and the global default registry is shared by the whole process; a normal `*App` and
a `NewTestApp` in the same test binary would both try to register `orders_http_requests_total`
into it and the second one would crash the test. A registry that belongs to this one `*Metrics`
instance can't collide with anything.

Now the middleware, and the cardinality trap it exists to avoid:

```go
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.requestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		m.requestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
```

`r.Pattern` — not `r.URL.Path` — is the label. Go 1.22+'s `http.ServeMux` records which registered
pattern actually matched a request onto `r.Pattern` (`"GET /orders/{id}"`, not `/orders/<the
actual uuid>`). Using the raw path instead would give `GET /orders/<uuid>` its own, permanently
distinct label value for *every order ever created* — exactly the unbounded-cardinality mistake
that turns a small Prometheus instance into a struggling one. A test proves the label is actually
bounded:

```go
func TestMetricsMiddlewareLabelsByRoutePatternNotRawPath(t *testing.T) {
	m := observability.NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.Middleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/orders/11111111-1111-1111-1111-111111111111", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `route="GET /orders/{id}"`) {
		t.Errorf("metrics output missing route=%q label, got:\n%s", "GET /orders/{id}", body)
	}
	if strings.Contains(body, "11111111-1111-1111-1111-111111111111") {
		t.Error("metrics output contains the raw order ID — route should be the pattern, not the path, to avoid unbounded label cardinality")
	}
}
```

`statusRecorder` here looks identical to `statusWriter` in `api/middleware.go` from
[chapter 10](10-api-layer.md) — that's not an oversight. They live in different packages for
different, narrow reasons (this one is `observability`'s own concern; that one is `api`'s), and
sharing five lines isn't worth a cross-package dependency just to avoid typing them twice.

## Tracing: spans that survive tracing being turned off

Create `observability/tracing.go`. The constructor is where the interesting decision lives:

```go
package observability

import (
	"context"
	"fmt"
	"net/http"

	"example.com/servoorders/config"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type Tracer struct {
	provider *sdktrace.TracerProvider
}

func NewTracer(cfg *Config) (*Tracer, error) {
	ctx := context.Background()
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName("servoorders"),
	))
	if err != nil {
		return nil, fmt.Errorf("observability: resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.OTLPEndpoint != "" {
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("observability: otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	return &Tracer{provider: sdktrace.NewTracerProvider(opts...)}, nil
}

func (t *Tracer) Stop(ctx context.Context) error {
	return t.provider.Shutdown(ctx)
}
```

Notice there's no `Init` here, even though `postgres.Store`, `redis.Cache`, and
`natsbroker.Publisher` all have one. That's deliberate, and it's a real ordering constraint worth
understanding rather than a style choice: `api.Server`'s own constructor needs a fully-working
`*Tracer` immediately, to wrap its handler with tracing middleware — and servo only guarantees a
*dependency's constructor* has already returned by the time a *dependent's* constructor runs, not
that its `Init` has too. `Init` calls all happen later, together, in one `errgroup`. Splitting this
into `New` (build) + `Init` (start) the way `postgres.Store` does would hand `api.Server` a
`*Tracer` whose provider field is still `nil`. Doing all the setup inside `New` itself — the same
shape every NewConfig already uses — sidesteps the whole question.

The `if cfg.OTLPEndpoint != ""` branch is the other thing worth noticing: with no endpoint
configured, `NewTracer` still returns a fully working `*Tracer` — spans are created, sampled, and
given real trace and span IDs — they're just never exported anywhere. Tracing is opt-in this way
on purpose; a service shouldn't refuse to start just because nobody configured a trace backend for
it:

```go
func TestNewTracerWithNoEndpointStillCreatesSpans(t *testing.T) {
	tracer, err := observability.NewTracer(&observability.Config{OTLPEndpoint: ""})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	t.Cleanup(func() { tracer.Stop(context.Background()) })

	var sawSpanContext bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		sawSpanContext = trace.SpanContextFromContext(r.Context()).IsValid()
	})
	handler := tracer.Middleware(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orders/abc", nil))

	if !sawSpanContext {
		t.Error("expected a valid span context inside the handler even with OTLPEndpoint unset — spans are still created, just never exported")
	}
}
```

One more thing worth testing explicitly, because the opposite assumption is an easy one to make:
`otlptracehttp.New` doesn't dial the endpoint at construction time at all — export happens later,
asynchronously, in the background. `NewTracer` succeeds immediately even against a garbage address:

```go
func TestNewTracerRejectsAnUnreachableEndpointOnlyAtExportTimeNotConstruction(t *testing.T) {
	tracer, err := observability.NewTracer(&observability.Config{OTLPEndpoint: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewTracer: %v, want it to succeed even with an unreachable endpoint", err)
	}
	tracer.Stop(context.Background())
}
```

Last, the middleware — reusing the same route-not-path decision from the metrics middleware above,
for a span name instead of a label:

```go
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "http.request",
		otelhttp.WithTracerProvider(t.provider),
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return operation
		}),
	)
}
```

## Wire both into api.Server

`New` grows two more parameters, and the middleware chain grows two more layers:

```go
func New(
	cfg *Config,
	orders *service.OrderService,
	authSvc *service.AuthService,
	issuer *auth.Issuer,
	metrics *observability.Metrics,
	tracer *observability.Tracer,
) *Server {
	// ... routes unchanged ...

	handler := loggingMiddleware(mux)
	handler = metrics.Middleware(handler)
	handler = tracer.Middleware(handler)
	handler = recoverMiddleware(handler)

	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: handler}
	return s
}
```

`/metrics` can't be reached the same way `/healthz` and `/readyz` were in
[chapter 10](10-api-layer.md) — `Metrics` lives *inside* `api.Server`, not on the outer `App` — so
`Server` needs to expose it, and `main.go` reaches for it directly, the same way `app_test.go`
already reaches `app.server` (both files are `package main`):

```go
// api/server.go
func (s *Server) MetricsHandler() http.Handler {
	return s.metrics.Handler()
}
```

```go
// cmd/orders/main.go
adminSrv := admin.New(app.apiConfig.AdminAddr, app, app.server.MetricsHandler())
```

## Try it — logs, metrics, and a real trace in Jaeger

Add Jaeger to `deploy/docker-compose.yml` (it accepts OTLP over HTTP natively, no separate
collector needed):

```yaml
jaeger:
  image: jaegertracing/all-in-one:1.60
  environment:
    COLLECTOR_OTLP_ENABLED: "true"
  ports:
    - "16686:16686" # web UI
    - "4318:4318"   # OTLP over HTTP
```

Start everything, including `OTLP_ENDPOINT` this time:

```
$ make up
$ POSTGRES_DSN=... REDIS_ADDR=... NATS_URL=... JWT_SECRET=... \
  OTLP_ENDPOINT=localhost:4318 \
  go run ./cmd/orders
```

Log lines are now structured JSON:

```
{"time":"2026-08-27T14:44:55.548596+02:00","level":"INFO","msg":"request","method":"POST","path":"/auth/login","status":200}
```

After logging in and creating an order, `/metrics` on the admin port shows real data:

```
$ curl -s http://localhost:8081/metrics | grep orders_http_requests_total
orders_http_requests_total{method="POST",route="POST /auth/login",status="200"} 1
orders_http_requests_total{method="POST",route="POST /orders",status="201"} 1
```

And Jaeger (`http://localhost:16686` in a browser, or its query API) has a real trace, with real
semantic HTTP attributes attached automatically by `otelhttp` — nothing hand-instrumented:

```
$ curl -s "http://localhost:16686/api/traces?service=servoorders&limit=1" | python3 -m json.tool
```
```
operation: POST /orders
duration (us): 2470
  http.request.method = POST
  http.response.status_code = 201
  url.path = /orders
```

The span is named `POST /orders` — the route pattern, exactly as configured — not a path with a
UUID baked into a hundred different span names.

## Diagnostics

- **No traces show up in Jaeger even though `OTLPEndpoint` is set correctly** — the SDK batches
  spans and exports on an interval (a few seconds by default), not immediately on every request.
  Give it several seconds after the request before concluding it isn't working; then check the
  service's own logs for OTLP export errors, and confirm Jaeger's OTLP port (`4318`) is actually
  the one the endpoint points at, not its UI port (`16686`).
- **`/metrics` shows a metric with far more label combinations than expected** — almost always a
  label built from something request-specific (a raw path, a user ID, a request ID) rather than a
  bounded set of values. `route` here is safe specifically because `r.Pattern` only ever takes one
  of a handful of registered values, never one per request.
- **Log lines are plain text, not JSON, right at process startup** — a small, known gap: anything
  logged before `NewLogger` runs uses the unconfigured default handler. Nothing in this codebase is
  in that window, because every component that logs depends on the logger and so cannot be built
  first — but a library logging from an `init` function would be, and it is worth knowing the gap
  exists rather than assuming every line is guaranteed structured.

## Do's and don'ts

- **Do** treat "would this label's value be unbounded" as a question to ask before adding any new
  Prometheus label, every time — not just for HTTP routes. A user ID, an order ID, a raw error
  message: all classic ways this goes wrong later.
- **Do** let spans get created even when there's nowhere to export them. A service that behaves
  differently depending on whether tracing infrastructure exists is harder to reason about, not
  easier.
- **Don't** log a full request or response body at `INFO` by default — `loggingMiddleware` only
  ever logs method, path, and status, deliberately, going back to
  [chapter 10](10-api-layer.md#dos-and-donts).
- **Don't** reach for a global metrics/tracer registry out of habit. It's the more common pattern
  in small single-instance programs, but it's also exactly what makes two `*App`s (or an `*App` and
  a `NewTestApp`) in the same process collide.

## Next

[Chapter 14: Resilience](14-resilience.md) — a circuit breaker around the cache, and a rate
limiter in front of the API.
