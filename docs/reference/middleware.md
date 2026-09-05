# middleware package

```go
import "github.com/okian/servo/v3/middleware"
```

**Who this is for:** anyone attaching the commodity middleware instead of writing it.

Everything here is an ordinary graph node with the standard
`Middleware(next http.Handler) http.Handler` method, attached in the spec with
[`servo.Use[T](selector...)`](http.md) like middleware you write yourself — same wrap-order rules,
same group/route selectors, same `Override` story in tests. The package is standard library only.

Two shapes:

- **Zero-config** — the constructor takes nothing, so resolution finds it as-is and the `Use` line
  is the entire setup: `Recover`, `AccessLog`, `RequestID`, `Gzip`.
- **Configured** — the constructor takes a `*XxxConfig` that **your own provider** supplies, the
  same pattern as `*servo.HTTPConfig`. The config is a real Go struct built in your code, so
  fields like `RateLimitConfig.KeyFunc` are ordinary functions.

```go
// spec
servo.Build(
	servo.HTTP(
		servo.Use[*middleware.Recover](),                        // zero-config
		servo.Use[*middleware.CORS](servo.Group("default")),     // reads *middleware.CORSConfig
		servo.Use[*middleware.RateLimit](),                      // reads *middleware.RateLimitConfig
	),
)

// your module
func NewCORSConfig() *middleware.CORSConfig {
	return &middleware.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}
}
```

## `Recover`

Panic → 500 with the canonical body only; the panic value and stack go to slog. Re-panics
`http.ErrAbortHandler`, which is net/http's own "abort this connection" sentinel.

## `CORS` — `CORSConfig`

| Field | Meaning |
|---|---|
| `AllowedOrigins` | exact origins, or the single element `"*"`; required |
| `AllowedMethods` | defaults to every method servo routes |
| `AllowedHeaders` | defaults to echoing the preflight's request headers |
| `ExposedHeaders` | stamped on actual responses |
| `AllowCredentials` | with `"*"` the concrete origin is echoed — `*` is invalid alongside credentials |
| `MaxAge` | preflight cache lifetime; zero omits the header |

Preflights (`OPTIONS` + `Access-Control-Request-Method`) are answered with 204 and never reach the
handler. A disallowed origin gets no CORS headers at all — the browser enforces the block — and
`Vary: Origin` is always set.

## `RateLimit` — `RateLimitConfig`

A token bucket per key: `RPS` refills, `Burst` caps, exhausted keys get 429 with `Retry-After`.
`KeyFunc` decides what a key is — nil means the client IP (port stripped). The buckets are
in-process state, swept when idle; two replicas mean two buckets per key.

## `BodyLimit` — `BodyLimitConfig`

A declared `Content-Length` over `MaxBytes` is refused with 413 before the handler runs; chunked
bodies read through `http.MaxBytesReader`, so the cap holds on the read path too. Complements
`HTTPConfig.MaxBodyBytes`, which guards only routes that decode bodies.

## `AccessLog`

One `slog.Info` line per request — method, path, status, bytes, duration, remote — through
`slog.Default()` at call time, so it follows whatever logger main installs.

## `Timeout` — `TimeoutConfig`

`http.TimeoutHandler`: a handler exceeding `Limit` is cut off with 503 and `Body` (a JSON error by
default), and its request context is cancelled.

## `RequestID`

Reads or generates `X-Request-Id`, sets it on the response, and plants it in the context;
`middleware.RequestIDFromContext(ctx)` reads it back in handlers and extractors.

## `Gzip`

Compresses when the client accepts gzip. The decision is made at `WriteHeader` time: responses
already carrying a `Content-Encoding`, or with inherently compressed content types (`image/`,
`video/`, `audio/`, zip/gzip/zstd), pass through untouched. Sets `Vary: Accept-Encoding`.
