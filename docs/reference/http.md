# HTTP routes

**Who this is for:** anyone declaring a `//servo:` route, or deciding whether to.

servo can generate an HTTP server: the router, the typed request decoding, the response encoding
and the listener's lifecycle are all emitted into `servo_gen.go`, in the same sense the rest of the
file is — plain Go you can read, derived from declarations you wrote. The `servo` package itself
still ships no handler, no router and no middleware; what it gains for this feature is three
runtime types (`Json`, `HTTPStatus` with its `Status` table, and `HTTPConfig`) and one marker.

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
		return nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", req.Category, err)
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
  waits for startup.

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

Attached inside `servo.HTTP(...)`, at three levels:

| Attachment | Wraps |
|---|---|
| `servo.Use[*mw.Recover]()` | every group's whole handler stack |
| `servo.Use[*mw.Auth](servo.Group("internal"))` | one group's mux (`"default"` selects the default group) |
| `servo.Use[*mw.Audit](servo.Route("POST /order/{category}/"))` | one route |

Rules, all checked at generate time: the method's exact shape; one `Use` selects groups **or**
routes, never both; a `Group` selector must name a served group and a `Route` selector must match
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
		return nil, servo.Status.UNAUTHORIZED.Wrap(err) // 401 before the handler runs
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

`user` comes from the extractor, per request; `repo` still comes from the graph. An extraction
error short-circuits through the exact status contract below — the handler never runs. Generate-time
rules: the method's exact shape; one extractor per produced type; and a type that has **both** a
provider and an extractor is an error ("constructed once or extracted per request, never both").

## The handler signature

```go
func Name(ctx context.Context [, req *ReqT] [, deps...]) (servo.Json[T], error)
```

- The first parameter is `context.Context` — the request's context, so cancellation propagates and
  [scope keys](scopes.html) planted by the values on it resolve as usual.
- The **second parameter is the request struct** iff it is a pointer to a named struct that
  declares at least one `path`/`query`/`header`/`form`/`json` tag. The tags are what mark it;
  an untagged struct pointer in that position is an ordinary dependency.
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
| `servo.Stream(ct, r)` | `io.Copy` of the reader — files, pipes |
| `servo.Redirect(url), servo.Status.FOUND` | `Location` header; the 3xx status flows through the success path |
| `nil, servo.Status.NO_CONTENT` | status only, no body — a nil response always means "code alone" |
| `nil, nil` | an empty 200 — a nil error always means 200 |

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

Rules, each enforced at generate time:

- Body fields (JSON or form) are only allowed on `POST`, `PUT` and `PATCH`.
- A struct binds its body as a form **or** as JSON, never both.
- Every `path` tag must have a matching `{segment}` in the pattern, and every segment a matching
  tag — both directions.
- Bound fields must be exported, scalar, and unique per source.

The JSON body decodes first and the explicitly bound sources overwrite after, so a body cannot
smuggle a value into a path- or query-bound field. Bodies are read through `http.MaxBytesReader`,
capped by `HTTPConfig.MaxBodyBytes` (default 1 MiB).

## Status and errors

`servo.Status` is the table of HTTP statuses, spelled the way RFC 9110 and the IANA registry spell
them (`NOT_FOUND`, `UNPROCESSABLE_CONTENT`, `CONTENT_TOO_LARGE`). Each entry is a
`servo.HTTPStatus`, which implements `error` and constructs errors that carry it:

| Handler returns | Response |
|---|---|
| `servo.JSON(v), nil` | `200`, `v` as JSON |
| `servo.JSON(v), servo.Status.CREATED` | `201`, `v` as JSON — a 2xx status in the error position picks the success code |
| `nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", c, err)` | `404`, `{"error": "<your message>"}` |
| `nil, servo.Status.FORBIDDEN.New("category is closed")` | `403`, `{"error": "category is closed"}` |
| `nil, servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)` | `500`, `{"error": "Internal Server Error"}` — canonical text only, full error logged via `log/slog` |
| `nil, err` (no status in the chain) | `500`, same as above |

The split at 500 is deliberate: a 4xx message is written for the API's caller — that is what `New`
and `Wrapf` are for — while a 5xx wraps infrastructure detail that belongs in the log, not on the
wire. `errors.Is` and `errors.As` both see through the wrapping: `errors.Is(err,
servo.Status.NOT_FOUND)` holds, and so does `errors.Is(err, yourSentinel)` when the message used
`%w`.

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
| `CertFile`, `KeyFile` | both set → the server serves TLS |
| `MaxBodyBytes` | request-body cap; `<= 0` means the generated 1 MiB default |
| `ReadTimeout`, `WriteTimeout`, `IdleTimeout` | passed to `net/http.Server`; zero means none |
| `Groups` | one `servo.HTTPListener` (the same knobs, per group) for each declared group, keyed by name |

Declaring `servo.HTTP()` with no provider for `*servo.HTTPConfig` is a resolution diagnostic with
the standard needed-by chain, rooted at the marker.

## Lifecycle

The servers are emitted machinery, not graph nodes — the same standing scope registries have. Each
group's server is constructed at the end of `New` (wiring only; the listener is not bound yet —
though a declared group missing its `Groups` entry fails right here, with rollback), and in `Run`
every server binds explicitly, flips its readiness flag, and serves until the context ends.
`Ready` reports one entry per group (`"http"`, `"http:telemetry"`) meaning exactly "that listener
is bound". In `Shutdown` the servers stop **first**, before every singleton: they are the inbound
edges, and draining them (`net/http.Server.Shutdown`, under `servo.DefaultStopBudget`) is what
lets everything beneath quiesce. `Health` says nothing about them — a bound socket is not a health
claim.

## What this does not do

- **No content negotiation.** Responses are `application/json`; `Json[T]` is the only response
  shape.
- **No response-typed middleware.** `Middleware` wraps `http.Handler`s; nothing gives a wrapper
  typed access to a handler's decoded request or encoded response.
- **No opting out of the default group.** Every `servo.HTTP(...)` injector serves the default
  group's routes; a groups-only binary is not yet expressible.
