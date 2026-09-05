# HTTP batteries: shipped middleware and the response family — design

**Status:** approved (all decisions confirmed with the owner, 2026-09-05)
**Builds on:** `//servo:` HTTP directives + groups/middleware/extractors (both unreleased).

## Response family

One sealed base interface; encoding moves into the runtime so new kinds never touch the emitter:

```go
type Response interface{ write(w http.ResponseWriter, code int) error } // method unexported: sealed

type Json[T any] interface { Response; Value() T }   // servo.JSON(v), unchanged spelling
type Xml[T any]  interface { Response; Value() T }   // servo.XML(v)

func Text(s string) Response                          // text/plain; charset=utf-8
func HTML(s string) Response                          // text/html; charset=utf-8
func Blob(contentType string, data []byte) Response   // caller-set type; octet-stream when empty
func Redirect(url string) Response                    // Location header; pair with a 3xx Status
func Stream(contentType string, r io.Reader) Response // io.Copy body

func WriteResponse(w http.ResponseWriter, code int, res Response) error // the emitted adapter's bridge
```

- A handler's first result must implement `servo.Response`: the typed forms (`Json[T]`, `Xml[T]`)
  keep response schemas statically visible; plain `servo.Response` allows runtime choice.
- `nil` error stays exactly 200; `nil` response writes the code alone (so
  `return nil, servo.Status.NO_CONTENT` is the 204 idiom).
- The success-as-error window widens from `< 300` to `< 400`, so
  `return servo.Redirect(url), servo.Status.FOUND` flows through the respond path.
- The emitted `httpRespond` becomes non-generic (`servo.Response` parameter) and delegates to
  `servo.WriteResponse`; the emitted `httpWriteJSON` disappears (error bodies keep their own
  emitted encoder). The `servo` package now imports `net/http`/`encoding/xml`/`io` — still
  stdlib-only.

## Shipped middleware

New stdlib-only package `github.com/okian/servo/v3/middleware`. Every middleware is an ordinary
graph node with the standard `Middleware(next http.Handler) http.Handler` method, attached with
the existing `servo.Use[T](selector...)` — no new generator machinery: exported constructors in
dependency modules are already resolution candidates, and configurable ones take a `*XxxConfig`
node the **user's own provider** supplies (a real Go struct, so func fields like `KeyFunc` work).

| Middleware | Config | Behavior |
|---|---|---|
| `Recover` | none | panic → 500 `{"error":"Internal Server Error"}`, stack to slog |
| `CORS` | `CORSConfig{AllowedOrigins/Methods/Headers, ExposedHeaders, AllowCredentials, MaxAge}` | answers preflights itself (204, no next); stamps actual responses; echoes the origin (never `*`) when credentials are allowed; `Vary: Origin` |
| `RateLimit` | `RateLimitConfig{RPS float64, Burst int, KeyFunc func(*http.Request) string}` | hand-rolled token bucket per key (default: client IP), 429 + `Retry-After`; idle buckets swept periodically |
| `BodyLimit` | `BodyLimitConfig{MaxBytes int64}` | declared `Content-Length` over the cap → immediate 413; otherwise `http.MaxBytesReader` guards the read path |
| `AccessLog` | none | one slog line per request: method, path, status, bytes, duration, remote |
| `Timeout` | `TimeoutConfig{Limit time.Duration, Body string}` | `http.TimeoutHandler`; 503 with the body (JSON error default) |
| `RequestID` | none | reads or generates `X-Request-Id`, sets response header + context; `RequestIDFromContext(ctx)` accessor |
| `Gzip` | none | wraps when the client accepts gzip; skips responses already encoded or with incompressible types (`image/`, `video/`, `audio/`, zip/gzip) |

Being in the root module, `go test ./...` and the existing lint loop cover the package with no CI
changes.

## Example + docs

`examples/http` gains `servo.Use[*middleware.Recover]()`, a CORS attachment with a user config
provider, a `Text` endpoint and a `Redirect` endpoint, all asserted in the e2e. Docs: response
table + middleware section in `docs/reference/http.md`, a `docs/reference/middleware.md` page,
`servo-package.md` additions, README + CHANGELOG.

## Non-goals

- Response-typed middleware (wrapping decoded requests / typed responses).
- Distributed rate limiting; the bucket is per process.
- Content negotiation.
