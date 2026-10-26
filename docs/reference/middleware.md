# middleware package

```go
import "github.com/okian/servo/v3/middleware"
```

**Who this is for:** anyone attaching the commodity middleware instead of writing it.

Everything here is an ordinary graph node with the standard
`Middleware(next http.Handler) http.Handler` method, attached in the spec with
[`servo.Use`](http.md) — `servo.Use[T](selector...)` — like middleware you write yourself — same wrap-order rules,
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
		servo.Use[*middleware.RequestID](),                      // zero-config
		servo.Use[*middleware.Recover](),
		servo.Use[*middleware.AccessLog](),
		servo.Use[*middleware.RateLimit](),                      // reads *middleware.RateLimitConfig
		servo.Use[*middleware.CORS](servo.Group("default")),     // reads *middleware.CORSConfig
	),
)

// your module
func NewCORSConfig() *middleware.CORSConfig {
	return &middleware.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}
}
```

## Order

Declaration order is wrap order, outermost first, stacked server → group → route (see
[HTTP routes](http.md)). The shipped middleware work best in this order:

1. **`RequestID`** first, so the context everything inside it logs with carries the id: Recover's
   crash line, AccessLog's line, and your handlers' own `slog.*Context` calls. It cannot panic, so
   nothing is lost by putting it outside Recover.
2. **`Recover`** next, so it sees every panic below it.
3. **`AccessLog`** after them, ahead of everything that can refuse a request, so a 429, a 413 or a
   CORS preflight is still logged.
4. **The rest** — `RateLimit`, `BodyLimit`, `CORS`, `Gzip` — in whatever order suits. With `Gzip`
   inside AccessLog, the logged `bytes` are the compressed bytes on the wire.
5. **`Timeout`** only on the groups or routes whose responses are bounded, never ahead of a stream
   (see [its limits](#timeout--timeoutconfig)).

AccessLog's `route` is `r.Pattern`, which net/http's `ServeMux` sets on the request it receives.
A middleware between AccessLog and the mux that replaces the request (`r.WithContext` — a
group-level `Timeout`, an auth middleware planting a user) hides it, and the line then has no
`route`; route-level attachments run inside the mux and don't. With `Recover` first and
`RequestID` second, both lines still carry the request id (Recover reads it from the
`X-Request-Id` response header RequestID set), but Recover's loses the route.

## `Recover`

Panic → 500 with the canonical body only. The panic value and stack go to slog at ERROR, logged
with the request's context (so a context-aware handler can add a trace id) and with `method`,
`path`, `route` and `request_id` when they are known. A `Content-Length` the handler set is
dropped from the 500.

If the handler had already started the response — a status sent, part of a body written — the
500 can no longer be sent: the status is gone, and an error appended to the body would read as the
end of a 200. Recover then aborts with `panic(http.ErrAbortHandler)`, which net/http turns into a
dropped connection (a reset stream on HTTP/2), so the client sees the truncated response for the
failure it is. `http.ErrAbortHandler` itself — net/http's sentinel for a deliberate abort, which
the generated HTTP adapter uses to cut off a stream that failed midway — passes through untouched
and is not logged.

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
The buckets are in-process state; two replicas mean two buckets per key.

| Field | Meaning |
|---|---|
| `RPS` | sustained refill rate per key; positive and finite |
| `Burst` | bucket size, at least 1 |
| `KeyFunc` | what a client is; nil means `ClientIPKey()`, below |
| `MaxKeys` | how many keys are tracked at once; zero means 100,000 (about 12 MiB), negative is an error |

**The default key is the address the connection came from**: `r.RemoteAddr` without its port,
and an IPv6 address by its /64 — the block one host is routinely given, so a client cannot mint a
fresh bucket per address. **Behind a load balancer, an ingress or a CDN, that address is the
proxy's, and every client shares one bucket.** There, key by the client the proxy forwarded for:

```go
func NewRateLimitConfig() *middleware.RateLimitConfig {
	return &middleware.RateLimitConfig{
		RPS:     10,
		Burst:   20,
		KeyFunc: middleware.ClientIPKey(netip.MustParsePrefix("10.0.0.0/8")), // your proxies
	}
}
```

`ClientIPKey(trusted ...netip.Prefix)` starts at the peer and, while the address it holds is
inside a trusted prefix, walks `X-Forwarded-For` from the right; the first address no prefix covers
is the client. Entries left of it are whatever the client sent and are never read, so the header
cannot be used to choose a key, and an entry that is not an address ends the walk at the trusted
hop that wrote it. Trust exactly your proxies: a client inside a trusted prefix can name its own
key. With no prefixes it is the default key.

Memory and time stay bounded. A bucket is forgotten only once it has been idle long enough to
refill completely — `Burst/RPS`, and never under five minutes — so an idle spell never resets a
slow limit like five an hour, and each request forgets at most two such buckets, so no request pays
for a sweep of the table. At `MaxKeys` a new key evicts the least recently seen one, which starts
over with a full bucket if it returns: under a flood of distinct keys the limiter forgets clients
rather than grow without bound or turn them away.

## `BodyLimit` — `BodyLimitConfig`

A declared `Content-Length` over `MaxBytes` is refused with 413 before the handler runs; chunked
bodies read through `http.MaxBytesReader`, so the cap holds on the read path too. Complements
`HTTPConfig.MaxBodyBytes`, which guards only routes that decode bodies (and answers 413 there as
well).

## `AccessLog`

One `slog.Info` line per request through `slog.Default()` at call time, logged with the request's
context: `method`, `path`, `route` (the pattern the mux matched, such as `GET /items/{id}`),
`status`, `bytes`, `duration`, `remote` and `request_id`.

The line is written in a defer, so a request whose handler panicked is logged too, marked
`panicked=true`, before the panic carries on to Recover. Its status is the one the handler sent, or
500 if it sent none — what Recover answers. A response cut off on purpose with
`http.ErrAbortHandler` is marked `aborted=true` instead. `route` is absent when no route matched
and when a middleware between AccessLog and the mux replaced the request (see [Order](#order));
`request_id` is RequestID's, read from the context or, when RequestID runs inside AccessLog, from
the response header it set.

## `Timeout` — `TimeoutConfig`

`http.TimeoutHandler`: a handler exceeding `Limit` is cut off with 503 and `Body`, and its request
context is cancelled. The default body is a JSON error sent as `application/json`; a `Body` of your
own gets that type when it is valid JSON and the type net/http sniffs otherwise.

Cancelling is all it can do, and `http.TimeoutHandler` brings three limits with it:

- **It buffers the whole response** until the handler returns. Nothing reaches the client before
  then, and a large body costs its full size in memory.
- **It hides `http.Flusher` and `http.Hijacker`.** `http.ResponseController`'s `Flush` reports
  `http.ErrNotSupported`, so a streaming handler cannot stream and a websocket cannot upgrade.
- **A handler past its limit keeps running** until it returns. Its writes fail with
  `http.ErrHandlerTimeout`, but nothing stops it, and the server counts the request finished once
  the 503 is out — so `srv.Shutdown`, and the generated `Shutdown` with it, does not wait for the
  handler, which can outlive Shutdown and reach dependencies that have already stopped.

So attach Timeout, with `servo.Group` or `servo.Route`, to the routes whose responses are bounded,
never ahead of a streaming one — a route returning `servo.Stream`, an event stream, a websocket.
Bound those another way: their work through the request context (`context.WithTimeout`, or a
reader that stops when the context ends), and slow clients through write deadlines —
`HTTPConfig.WriteTimeout` for the whole listener, or `http.ResponseController.SetWriteDeadline`,
renewed as the stream goes, from middleware that holds the `ResponseWriter`.

## `RequestID`

Reads `X-Request-Id` when it is at most 128 bytes of printable ASCII and generates one otherwise,
sets it on the response, and plants it in the context;
`middleware.RequestIDFromContext(ctx)` reads it back in handlers and extractors, and Recover and
AccessLog log it as `request_id`.

## `Gzip`

Compresses when the client accepts gzip, reading `Accept-Encoding` with its weights: `gzip;q=0` is
a refusal, and `*` counts when gzip is not listed. The decision waits for the body. A response
that ends or is flushed before 1 KiB goes out as it is, since compressing so little saves nothing;
one that reaches 1 KiB is compressed unless its type, as set or as net/http would sniff it, is
compressed already (`image/`, `video/`, `audio/`, zip/gzip/zstd). Holding that first KiB delays
nothing net/http does not hold anyway.

Responses already carrying a `Content-Encoding`, with a body-less status (1xx, 204, 304), or
carrying a byte range (206, or any `Content-Range`) pass through untouched. A compressed response
drops `Content-Length` and `Accept-Ranges`, and every response gets `Vary: Accept-Encoding`.
Compressors are pooled, so a response costs a few KiB rather than the megabyte a fresh compressor
allocates. The writer keeps `http.Flusher` and `http.Hijacker`, and a flush sends everything
written so far.
