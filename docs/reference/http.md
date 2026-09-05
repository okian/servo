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
	servo.HTTP(),
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
//servo:<method> <pattern>
```

- It must be (part of) the **doc comment of an exported, top-level function**. Not a method, not
  unexported, not generic, not variadic — each violation is its own diagnostic.
- `<method>` is one of `get`, `post`, `put`, `patch`, `delete`, `head`, `options`.
- `<pattern>` is a Go 1.22+ [`net/http.ServeMux`
  pattern](https://pkg.go.dev/net/http#hdr-Patterns-ServeMux) without the method — servo prepends
  `METHOD ` when registering. The semantics are ServeMux's, verbatim: `{category}` matches one
  segment, `{rest...}` the remainder, `{$}` the exact path, and a trailing `/` matches the whole
  subtree.
- Several directives above one function register the same handler on several routes.
- The whole `//servo:` comment prefix is **reserved**: anything under it that doesn't parse as a
  valid directive is a generate-time error. A typo'd method must never silently leave a route
  unserved.

Routes are discovered module-wide, like providers. Which injector serves them is the spec's
decision: only a `servo.Build(...)` containing `servo.HTTP()` gets the server, so a module with an
HTTP injector and a worker injector emits it exactly once. At most one `servo.HTTP()` per spec.

Pattern conflicts are caught at generate time by registering every route on a scratch ServeMux —
the same code that would panic at startup — so `servo generate` fails with net/http's own message
instead of your process failing to boot.

## The handler signature

```go
func Name(ctx context.Context [, req *ReqT] [, deps...]) (servo.Json[T], error)
```

- The first parameter is `context.Context` — the request's context, so cancellation propagates and
  [scope keys](scopes.html) planted by the values on it resolve as usual.
- The **second parameter is the request struct** iff it is a pointer to a named struct that
  declares at least one `path`/`query`/`header`/`form`/`json` tag. The tags are what mark it;
  an untagged struct pointer in that position is an ordinary dependency.
- Everything after `ctx` (and the request struct, when present) is a **dependency**, resolved from
  the graph exactly as a constructor parameter would be — same precedence, same diagnostics, same
  needed-by chains. Dependencies must be singletons: a scoped type is rejected with the
  [widening](scopes.html) rule's reasoning, and the fix is the same — depend on the scope's
  accessor interface and `Acquire(ctx)` per request.
- The result is exactly `(servo.Json[T], error)`. `Json` is an interface, so the error path is a
  plain `return nil, err`.

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

Declaring `servo.HTTP()` with no provider for `*servo.HTTPConfig` is a resolution diagnostic with
the standard needed-by chain, rooted at the marker.

## Lifecycle

The server is emitted machinery, not a graph node — the same standing scope registries have. It is
constructed last (wiring only; the listener is not bound yet), and in `Run` it binds explicitly,
flips the readiness flag, and serves until the context ends. `Ready` reports an `"http"` entry
that is exactly "the listener is bound". In `Shutdown` the server stops **first**, before every
singleton: it is the inbound edge, and draining it (`net/http.Server.Shutdown`, under
`servo.DefaultStopBudget`) is what lets everything beneath it quiesce. `Health` says nothing about
it — a bound socket is not a health claim.

## What v1 does not do

- **No middleware seam.** There is no hook between the mux and your handler — no auth, logging or
  tracing wrapper injection yet. A handler needing per-request policy enforces it itself.
- **No content negotiation.** Responses are `application/json`; `Json[T]` is the only response
  shape.
- **No route-level subsetting.** `servo.HTTP()` serves every directive in the module; two
  injectors wanting different route sets is not yet expressible.
