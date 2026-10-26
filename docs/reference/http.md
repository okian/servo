# HTTP routes

**Who this is for:** anyone declaring a `//servo:` route, or deciding whether to.

servo can generate an HTTP server: the router, the middleware chain, the typed request decoding and
one adapter per route are emitted into `servo_gen.go`, in the same sense the rest of the file is —
plain Go you can read, derived from declarations you wrote. What is the same for every server —
building and binding a listener, draining it, writing a response, turning an error into a status —
is runtime code the generated file calls, in `github.com/okian/servo/v3/servoimpl`, written and
tested once instead of once per app. The `servo` package itself still ships no handler and no
router; what it gains for this feature is the sealed
[response family](servo-package.md#the-response-family), `HTTPStatus` with its `Status` table and
`StatusFor`, `HTTPConfig` with its per-group `HTTPListener`, and five markers — `HTTP`, `Group`,
`Use`, `Route`, `Extract`. The commodity middleware ships as its own package,
[`middleware`](middleware.md), as ordinary graph nodes rather than anything the runtime wires.

Two declarations opt in:

```go
// in the spec file
servo.Build(
	servo.HTTP(
		servo.Group("telemetry"),                    // extra listeners, one per group
		servo.Use[*mw.RequestID](),                  // middleware: every group
		servo.Use[*mw.Auth](servo.Group("telemetry")), // ... one group
	),
	servo.Extract[*mw.UserExtractor](),              // typed per-request handler params
)
```

```go
// on any exported top-level function in the module
//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, st *store.Store) (servo.Json[*OrderResp], error) {
	cat, err := st.Category(req.Category)
	if errors.Is(err, store.ErrNotFound) {
		return nil, servo.Status.NOT_FOUND.Newf("no category %q", req.Category)
	}
	if !cat.Open {
		return nil, servo.Status.FORBIDDEN.New("category is closed")
	}
	...
	return servo.JSON(resp), servo.Status.CREATED
}
```

A complete runnable service — every binding source, every status path, an end-to-end test against
a real listener — is [`examples/http`](https://github.com/okian/servo/tree/master/examples/http).

## The directive

```
//servo:<method> <pattern> [group]
```

- It must be (part of) the **doc comment of an exported, top-level function**. Not a method, not
  unexported, not generic, not variadic — each violation is its own diagnostic.
- `<method>` is one of `get`, `post`, `put`, `patch`, `delete`, `head`, `options`.
- `<pattern>` is a Go 1.22+ [`net/http.ServeMux`
  pattern](https://pkg.go.dev/net/http#hdr-Patterns-ServeMux) without the method — servo prepends
  `METHOD ` when registering. The semantics are ServeMux's, verbatim: `{category}` matches one
  segment, `{rest...}` the remainder, `{$}` the exact path, and a trailing `/` matches the whole
  subtree.
- The optional trailing token names the route's **group** (grammar `[A-Za-z0-9_-]+`); no token —
  or the literal `default` — means the default group. See Groups below.
- Several directives above one function register the same handler on several routes.
- A handler in a **`package main`** is served only by that package's own injector, because no
  other package can import it; every other injector skips it, and a main package whose injector
  declares no `servo.HTTP()` (or that has no injector at all) is a generate-time error at the
  directive, since nothing would ever serve the route.
- The whole `//servo:` comment prefix is **reserved**: anything under it that doesn't parse as a
  valid directive is a generate-time error. A typo'd method must never silently leave a route
  unserved.

Routes are discovered module-wide, like providers. Which injector serves them is the spec's
decision: only a `servo.Build(...)` containing `servo.HTTP(...)` gets servers, so a module with an
HTTP injector and a worker injector emits them exactly once. At most one `servo.HTTP()` per spec.

Pattern conflicts are caught at generate time by registering every route on a scratch ServeMux —
the same code that would panic at startup — so `servo generate` fails with net/http's own message
instead of your process failing to boot. The check is per group: two groups are two servers, so
the same method and pattern may exist on both.

## Groups — one listener per port

A group is a named listener: `//servo:get /healthz telemetry` puts the route on whatever
`HTTPConfig.Groups["telemetry"]` describes, its own port, its own optional TLS. The default group
(routes with no token) uses `HTTPConfig`'s flat fields. The shape this exists for:

- default group on 9000 — the public API
- `telemetry` on 9001 — health, metrics
- `internal` on 9002 — back-to-back traffic

Groups are **declared per injector** with `servo.Group("name")` inside `servo.HTTP(...)`:

- An injector serves the default group plus every group it declares — and skips the rest, which
  is how two binaries in one module serve different route subsets.
- A group token that **no** spec in the module declares is a generate-time error at the
  directive: a typo must not silently strand a route.
- A declared group missing from `HTTPConfig.Groups` fails `New` with an error naming it — group
  names are generate-time facts, port numbers are runtime values, so this is the one check that
  waits for `New`. It runs after every constructor and before any `Init`, so a wrong name fails
  before a database is ever connected.

Each group's server reports independently: `Ready` has one entry per group (`"http"`,
`"http:telemetry"`), and `Shutdown` stops every group server before any node.

## Middleware — `servo.Use`

A middleware is an ordinary graph node — constructed with its own dependencies, Override-able in
tests — with one structurally-required method:

```go
func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), key{}, newID()) // context change
		w.Header().Set("X-Request-Id", ...)                   // pre/post work
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

Or the node *is* the middleware: a plain `func(http.Handler) http.Handler` — the shape
`otelhttp.NewMiddleware`, chi's middleware and gorilla/handlers ship — is attached as it is, and the
generated chain calls it where it would have called a `Middleware` method. Give it a named type, so
a provider can return it as a node:

```go
type OTel func(http.Handler) http.Handler

func NewOTel() OTel { return otelhttp.NewMiddleware("orders") }
```

`servo.Use[OTel]()` then needs no wrapper struct. A function type that declares a `Middleware`
method anyway is attached through the method, the explicit form.

Attached inside `servo.HTTP(...)`, at three levels:

| Attachment | Wraps |
|---|---|
| `servo.Use[*mw.Recover]()` | every group's whole handler stack |
| `servo.Use[*mw.Auth](servo.Group("internal"))` | one group's mux (`"default"` selects the default group) |
| `servo.Use[*mw.Audit](servo.Route("POST /order/{category}/"))` | one route |

Rules, all checked at generate time: the method's — or the function's — exact shape; one `Use`
selects groups **or** routes, never both; a `Group` selector must name a served group and a `Route` selector must match
a served route exactly. **Wrap order is declaration order, outermost first**, stacked
server → group → route — the first `Use` in the spec is the outermost wrapper, exactly like a
hand-written `recover → limiter → tracer → mux` chain. Handlers and extractors run innermost, so
they see every context value the middleware planted.

The commodity middleware ships in
[`github.com/okian/servo/v3/middleware`](middleware.md) — Recover, CORS, RateLimit, BodyLimit,
AccessLog, Timeout, RequestID, Gzip — as ordinary graph nodes: the zero-config ones need only the
`Use` line, the configurable ones read a `*XxxConfig` node your own provider supplies.

## Extractors — `servo.Extract`

An extractor turns a handler parameter into a **typed per-request value**. It is a graph node with
a by-name method (the result type varies per extractor, so no single interface could match it —
the same trade `ScopeKey` makes):

```go
func (e *UserExtractor) Extract(r *http.Request) (*auth.User, error) {
	u, err := e.issuer.Verify(r.Header.Get("Authorization"))
	if err != nil {
		return nil, servo.Status.UNAUTHORIZED.New("invalid or expired token") // 401 before the handler runs
	}
	return u, nil
}
```

Declared top-level in `Build` as `servo.Extract[*mw.UserExtractor]()`. From then on, **every
handler parameter of the method's result type** is extracted:

```go
//servo:get /order/{id}
func GetOrder(ctx context.Context, req *GetReq, user *auth.User, repo *repo.Repository) (servo.Json[*Order], error)
```

`user` comes from the extractor, per request; `repo` still comes from the graph. Extractors run
before the request struct is decoded, so a rejected credential costs nothing — the body is never
read — and a malformed body cannot turn a 401 into a 400. An extraction error short-circuits
through the exact status contract below; the handler never runs. Generate-time
rules: the method's exact shape; one extractor per produced type; and a type that has **both** a
provider and an extractor is an error ("constructed once or extracted per request, never both").

## The handler signature

```go
func Name(ctx context.Context [, req *ReqT] [, deps...]) (R, error)
```

- The first parameter is `context.Context` — the request's context, so cancellation propagates and
  [scope keys](scopes.html) planted by the values on it resolve as usual.
- The **second parameter is the request struct** iff it is a pointer to a named struct that
  declares at least one `path`/`query`/`header`/`form`/`json` tag. The tags are what mark it;
  an untagged struct pointer in that position is an ordinary dependency. A type the graph
  provides — by a constructor, a `servo.Value`, a scope, an extractor, or a `//servo:config`
  struct it points at — can never be the request struct: it would be filled from whatever the
  client sent in place of the value the graph builds, so generate refuses it, naming the provider
  (the same rule as an extractor's "constructed once or extracted per request, never both"). Give
  the route a request type of its own and take the provided one after it.
- Everything after `ctx` (and the request struct, when present) is either an **extracted
  parameter** — its type matches a declared extractor's result, produced per request — or a
  **dependency**, resolved from the graph exactly as a constructor parameter would be: same
  precedence, same diagnostics, same needed-by chains. Dependencies must be singletons: a scoped
  type is rejected with the [widening](scopes.html) rule's reasoning, and the fix is the same —
  depend on the scope's accessor interface and `Acquire(ctx)` per request.
- The result is `(R, error)` where `R` is a member of the sealed response family: the typed
  wrappers `servo.Json[T]` / `servo.Xml[T]` (the signature carries the schema), or plain
  `servo.Response` when the handler picks its encoding at runtime. All are interfaces, so the
  error path is a plain `return nil, err`.

## Responses

Every kind is constructed in the handler and encoded by the runtime, so the emitted adapter is the
same for all of them:

| Return | Wire |
|---|---|
| `servo.JSON(v)` | `application/json` |
| `servo.XML(v)` | `application/xml; charset=utf-8` |
| `servo.Text(s)` | `text/plain; charset=utf-8` |
| `servo.HTML(s)` | `text/html; charset=utf-8` |
| `servo.Blob(ct, data)` | caller's content type (`application/octet-stream` when empty) |
| `servo.Stream(ct, r)` | `io.Copy` of the reader — files, pipes, an upstream body; the reader is closed after |
| `servo.Redirect(url), servo.Status.FOUND` | `Location` header; the 3xx status flows through the success path |
| `nil, servo.Status.NO_CONTENT` | status only, no body — a nil response always means "code alone" |
| `nil, nil` | an empty 200 — a nil error always means 200 |

`servo.JSON` and `servo.XML` encode the whole payload before the status line goes out, so a payload
that does not encode — a NaN, a channel, a `MarshalJSON` that fails — is answered with a `500` and
the standard error body, and logged, rather than reaching the client as the success status over an
empty body.

A `servo.Stream` is copied after its status is committed, so a reader that fails partway can no
longer become an error status. Instead the server breaks the connection (on HTTP/2, resets the
stream), and the client sees a truncated response as truncated — the chunked body never ends —
rather than as a complete one. The response owns its reader: once the copy ends, however it ends,
the reader is closed if it is an `io.Closer`, which gives back a file's descriptor or an upstream
response's pooled connection. So don't close it in the handler: a deferred `Close` runs before the
copy starts. A `Stream` returned beside an error status (400 and up) is never written and never
closed; on an error path, close the reader yourself and return `nil`.

## Request binding

| Tag | Source | Types |
|---|---|---|
| `path:"category"` | `r.PathValue("category")` | scalars |
| `query:"page"` | the URL query | scalars |
| `header:"X-User"` | the request header | scalars |
| `form:"stars"` | urlencoded or multipart form body | scalars |
| `json:"item"`, or no tag | JSON request body | anything `encoding/json` decodes |

Scalars are `string`, `bool`, the `int`/`uint` families and `float32`/`float64`. Non-string
scalars get exact, generated `strconv` parsing; malformed input is a `400` naming the source
(`query parameter "page" is not a valid int`). An absent query/header/form value leaves the field
at its zero value.

A route that binds a query field parses the whole query string strictly first, with
`url.ParseQuery`: a query `net/url` refuses — a bad escape such as `%zz`, a `;` separator, more
parameters than it allows — is a `400` with `malformed query string`. `r.URL.Query()` would have
dropped the error and left every bound field at its zero value, answering `200` to a request it
never understood.

Rules, each enforced at generate time:

- Body fields (JSON or form) are only allowed on `POST`, `PUT` and `PATCH`.
- A struct binds its body as a form **or** as JSON, never both.
- Every `path` tag must have a matching `{segment}` in the pattern, and every segment a matching
  tag — both directions.
- Bound fields must be exported, scalar, and unique per source.

The JSON body decodes first and the explicitly bound sources overwrite after, so a body cannot
smuggle a value into a path- or query-bound field. Bodies are read through `http.MaxBytesReader`,
capped by `HTTPConfig.MaxBodyBytes` (default 1 MiB); a body over the cap is a `413` with
`request body too large`, distinct from the `400` a malformed one earns.

## Status and errors

`servo.Status` is the table of HTTP statuses, spelled the way RFC 9110 and the IANA registry spell
them (`NOT_FOUND`, `UNPROCESSABLE_CONTENT`, `CONTENT_TOO_LARGE`). Each entry is a
`servo.HTTPStatus`, which implements `error` and constructs errors that carry it:

| Handler returns | Response |
|---|---|
| `servo.JSON(v), nil` | `200`, `v` as JSON |
| `servo.JSON(v), servo.Status.CREATED` | `201`, `v` as JSON — a 2xx status in the error position picks the success code |
| `nil, servo.Status.NOT_FOUND.Newf("no category %q", c)` | `404`, `{"error": "no category \"tea\""}` |
| `nil, servo.Status.FORBIDDEN.New("category is closed")` | `403`, `{"error": "category is closed"}` |
| `nil, servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)` | `500`, `{"error": "Internal Server Error"}` — canonical text only, full error logged via `log/slog` |
| `nil, err` (no status in the chain) | `500`, same as above |
| `nil, err` wrapping `servo.ErrScopeFull` or `servo.ErrScopeClosed` | `503`, `Retry-After: 1`, `{"error": "Service Unavailable"}` — a [scope](scopes.md) shedding load, logged at Debug |
| `nil, servo.HTTPStatus{}`, or any code outside 100–999 | `500`, canonical text — no status at all, logged as the handler's bug |

The split at 500 is deliberate: a 4xx message is written for the API's caller, while a 5xx wraps
infrastructure detail that belongs in the log, not on the wire. `errors.Is` and `errors.As` both
see through the wrapping: `errors.Is(err, servo.Status.NOT_FOUND)` holds, and so does
`errors.Is(err, yourSentinel)` when the error wraps it.

A status in the chain always wins: the 503 for a scope's refusal applies only when the handler
returned the refusal without choosing a status of its own, so
`servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)` around one is still a 500.

An [extractor](#extractors--servoextract) fails through the same table, with one difference: it
returns a value or an error, never a response, so a status under 400 from an extractor is written
as the code alone — there is nothing to carry a body or a `Location` with it.

### Exactly what the client reads

Every error body is `{"error": "<text>"}`, and the text is one of these, never anything else:

- **Under a 4xx status: the returned error's `Error()`, all of it.** That is what `New` wrote, what
  `Newf` formatted — and, for `Wrap`, the wrapped error's own message, every layer of it; for
  `Wrapf` or a `%w` in `Newf`, the text of each wrapped error along with yours. So never put a 4xx
  around an error you did not write for the caller: a driver, a client library or the filesystem
  names tables, constraints, hosts and paths, and `Wrap`ping one sends a Postgres constraint name to
  whoever asked. Match the cause with `errors.Is`, answer in words of your own with `New` or `Newf`,
  and log the cause yourself if you need it. `Wrap` is for errors whose every word is already for
  the caller, such as a domain validation error.
- **Under a 5xx status, or with no status in the chain: the canonical text** — `Internal Server
  Error`, `Bad Gateway`, `Service Unavailable` — while the error itself goes only to the log.
- **The generated decoding's own messages:** `<source> is not a valid <type>` for a bad scalar
  (never `strconv`'s error), `malformed request body`, `malformed form body` and
  `malformed query string` with a `400`, and `request body too large` with a `413`.

Two answers carry no body: a 2xx or 3xx status in the error position, whose response is the
handler's own, and the `499` written when the client has already gone (below).

### The table, and codes it leaves out

`servo.Status` is a table to read, never to assign to. Go cannot make a struct variable read-only,
so `servo.Status.NOT_FOUND = …` compiles — and changes what that name means to every handler and
adapter in the process. For a code the table leaves out — nginx's 499, a status an upstream service
answered — `servo.StatusFor(code)` returns the table's own entry when there is one
(`StatusFor(404) == servo.Status.NOT_FOUND`) and a status carrying the bare code otherwise, whose
canonical text is `HTTP <code>`.

### When the request ends first

A handler that returns `ctx.Err()` after its client disconnected did exactly what it should, so it
is not reported as a failure. When the request's context was cancelled by the time the handler
returned, the adapter logs at Debug and writes a bare `499` (nginx's Client Closed Request): no
client reads it, but an access log or a metrics middleware wrapping the writer records a
disconnect rather than a 200 or a 500. A deadline is not a departure — a handler cut off by a
timeout middleware's deadline still counts as a 500 — and a request the server itself cancelled,
because `Shutdown`'s drain ran out of time (see [Lifecycle](#lifecycle)), is a `503`, logged at
Warn.

### What gets logged

Every line goes through `log/slog`'s default logger with the request's context, so a handler that
reads request or trace ids off the context — the shipped RequestID middleware's, OpenTelemetry's —
attaches them. The messages are constant; the request is in the attributes `handler`, `route`,
`status` and `error`, so a query can match on them.

| Event | Level | Message |
|---|---|---|
| a 5xx, or an error with no status | Error | `servo: handler failed` |
| the zero `HTTPStatus`, a code outside 100–999 | Error | `servo: handler failed with an HTTPStatus that is no status code` |
| a JSON or XML payload that did not encode | Error | `servo: response did not encode` |
| a write that failed after the status line — the connection is then broken | Error | `servo: writing the response failed` |
| a body under a status that forbids one (204, 304) | Error | `servo: response body refused by its status` |
| the client gone before the handler answered, or mid-response | Debug | `servo: client went away before the handler answered`, `servo: response cut short: the request ended first` |
| a scope's refusal | Debug | `servo: scope refused the request` |
| a request cancelled by an expired drain | Warn | `servo: request cancelled: the shutdown drain ran out of time` |

The servers log a few lines of their own at Warn, each saying what is wrong and what to set: a
listener started with nothing bounding its header read, a `ShutdownDelay` no shorter than what is
left of the shutdown deadline, only one of `CertFile` and `KeyFile` set, and a key pair that
failed to reload.

## The config

The emitted server reads a `*servo.HTTPConfig` node. servo never constructs one — you write an
ordinary provider, and where the values come from stays your business:

```go
func NewHTTPConfig() (*servo.HTTPConfig, error) {
	return &servo.HTTPConfig{IP: "127.0.0.1", Port: 8080}, nil
}
```

| Field | Meaning |
|---|---|
| `IP` | interface to bind; empty means all interfaces |
| `Port` | TCP port |
| `CertFile`, `KeyFile` | both set → the server serves TLS; only one set → plain HTTP, with a warning |
| `MaxBodyBytes` | request-body cap; `<= 0` means the generated 1 MiB default |
| `MaxHeaderBytes` | cap on the request line and headers; zero means net/http's default, 1 MB |
| `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` | passed to `net/http.Server`; zero means none, except that a zero `ReadHeaderTimeout` or `IdleTimeout` falls back to `ReadTimeout`. Bound the header read on anything internet-facing: without it a client that never finishes its headers holds the connection open indefinitely |
| `ShutdownDelay` | how long the server goes on serving after `Shutdown` drops its readiness, before closing its listener — see [Lifecycle](#lifecycle) |
| `Groups` | one `servo.HTTPListener` (the same knobs, per group) for each declared group, keyed by name. A group does not inherit the flat fields' values: give each its own timeouts |

Declaring `servo.HTTP()` with no provider for `*servo.HTTPConfig` is a resolution diagnostic with
the standard needed-by chain, rooted at the marker; a provider that returns nil fails `New`.

Zero timeouts keep meaning what they mean on `net/http.Server`, so the servers do not impose
defaults of their own. Instead each logs one warning when it starts listening with nothing
bounding its header read — `ReadHeaderTimeout`, or `ReadTimeout` standing in for it — because a
few hundred clients that never finish their headers are enough to exhaust it. `WriteTimeout`
bounds a whole response, a `Stream` included, so a route that streams for longer than any sensible
limit needs a listener — a group — of its own.

The TLS key pair is loaded while `New` builds the server, so a missing, unreadable or mismatched
file fails `New`, before any `Init` runs, with an error naming the server and both files — rather
than surfacing in `Run`, after every `Init`, as a server that never started. From then on the files
are looked at, at most every ten seconds, during handshakes, and a changed pair is loaded without
a restart: a rotated certificate, from cert-manager or a Kubernetes secret, goes into service by
itself. A pair that fails to load — the certificate written before its key — is logged once and
retried, and the previous certificate goes on being served. Every other setting is read once, by
`New`.

## Lifecycle

The servers are emitted machinery, not graph nodes — the same standing scope registries have. Each
group's server is constructed after every node and before the Init phase: wiring only, the
listener is not bound yet, though a declared group missing its `Groups` entry or a key pair that
does not load fails right here, with the same rollback an `Init` failure gets, before any `Init`
has run. In `Run` every server binds explicitly, flips its readiness flag, and serves until the
context ends; a port already in use is `Run`'s error, naming the server. `Ready` reports one entry
per group (`"http"`, `"http:telemetry"`), ok from the moment the listener is bound until
`Shutdown` takes the group out of service. `Health` says nothing about them — a bound socket is
not a health claim. For tests, the App gains one method: `HTTPHandler(group)` returns a group's
routed, middleware-wrapped handler for `httptest`, with no listener bound — see
[Generated API](generated-api.md#httphandler).

Request contexts descend from a context of the server's own, not from the one `Run` is given, so
the signal that ends `Run` does not cancel the requests a graceful drain is about to let finish.

In `Shutdown` the servers stop **first**, before every singleton: they are the inbound edges, and
draining them is what lets everything beneath quiesce. Every group stops at once, each in four
steps:

1. **Readiness drops.** From here `Ready` reports the group failed, so a readiness probe takes the
   instance out of rotation.
2. **The server keeps serving for its `ShutdownDelay`.** A load balancer, or Kubernetes updating a
   Service's endpoints, takes a few seconds to stop routing to an instance that is going away; a
   listener that closed first would turn those last requests into connection errors. A server
   that never bound was never in rotation and skips the delay, and the delay ends early when
   `Shutdown`'s context does.
3. **The drain.** `net/http.Server.Shutdown` closes the listener and waits for the requests in
   flight, for whatever is left of the deadline on `Shutdown`'s context — or for
   `servo.DefaultStopBudget` when the context has none.
4. **What the drain could not finish is cut off.** Every remaining request's context is cancelled
   and every connection closed, so a handler that respects its context stops before the
   dependencies behind it do; the group is reported abandoned, because one that ignores its
   context may still be running.

The groups' delays and drains overlap: two groups with five seconds each wait five, not ten. The
drain may use the whole deadline, and whatever stops after the servers gets only what is left —
size `Shutdown`'s deadline for the delay, the slowest request you mean to let finish, and your
components' own stops. A delay no shorter than what is left of the deadline leaves nothing to
drain with, and the server warns when it starts one. The Kubernetes side of this — the grace
period it all has to fit in — is in
[Lifecycle: HTTP servers in the shutdown sequence](lifecycle.md#http-servers-in-the-shutdown-sequence).

## What this does not do

- **No content negotiation.** The handler picks its encoding — a typed wrapper, or a
  `servo.Response` chosen at runtime — and nothing inspects `Accept` to pick one for it.
- **No response-typed middleware.** `Middleware` wraps `http.Handler`s; nothing gives a wrapper
  typed access to a handler's decoded request or encoded response.
- **No opting out of the default group.** Every `servo.HTTP(...)` injector serves the default
  group's routes; a groups-only binary is not yet expressible.
