# HTTP groups, middleware, and extractors — design

**Status:** approved (all decisions confirmed with the owner, 2026-09-05)
**Builds on:** the `//servo:` HTTP directives feature (commit `339dde3`), still unreleased —
signature changes to `servo.HTTP()` are free.

## Problem

One `servo.HTTP()` injector currently serves every module route on one listener, with no
middleware seam. Real services need: a public API port, a telemetry/health port, an internal port;
context-mutating and pre/post middleware; and typed per-request values (`user *auth.User`)
delivered as handler parameters.

## Design

### Groups (multi-port)

- A route names its group with one optional trailing token: `//servo:get /healthz telemetry`.
  No token = the **default group**. The literal token `default` is also the default group.
  Token grammar: `[A-Za-z0-9_-]+`, and not colliding with anything else on the line.
- Groups are **declared per injector**: `servo.HTTP(servo.Group("telemetry"))`. An injector
  serves the default group plus every group it declares. Routes in undeclared groups are simply
  not served by that injector (that is the per-injector subsetting feature).
- Typo protection is module-wide: a group token that **no** spec in the module declares is a
  generate-time error at the directive's position.
- Duplicate `Group("x")` in one `HTTP(...)`, empty name, name `default`, or a name failing the
  token grammar: parse-time errors.
- Each group is its own emitted server: own `http.ServeMux`, own listener, own TLS, own body
  limit. Route uniqueness and ServeMux conflict probing are **per group** — `POST /order` on the
  default group and on `internal` are two different servers and may coexist.

### Listener config

`servo.HTTPConfig`'s existing flat fields stay the **default group's** listener. It gains:

```go
type HTTPListener struct {
	IP       string
	Port     uint16
	CertFile string
	KeyFile  string
	MaxBodyBytes int64
	ReadTimeout, WriteTimeout, IdleTimeout time.Duration
}

type HTTPConfig struct {
	/* existing flat fields = the default group's listener */
	Groups map[string]HTTPListener // named groups
}
```

A declared group missing from `Groups` is a construction-time error naming the group (New fails
with rollback). Group names are known at generate time; port numbers are runtime values, so this
check is a runtime check by nature.

### Wrap middleware (Kind A — context values, pre/post)

A middleware is an ordinary graph node with a structurally-required method:

```go
func (m *Tracing) Middleware(next http.Handler) http.Handler
```

Attached in the spec, inside `servo.HTTP(...)`:

```go
servo.Use[*mw.Recover](),                                  // server-level: every group
servo.Use[*mw.Auth](servo.Group("internal")),              // group-level
servo.Use[*mw.Audit](servo.Route("POST /order/{category}/")), // route-level
```

- Wrap order: **declaration order, outermost first**, stacked server → group → route.
  Emission applies wraps in reverse declaration order so the first-declared is outermost.
- A `Use` selects groups **or** routes, never both in one call; no selector = server-level.
- `servo.Group("x")` inside `Use` must name a served group (`default` allowed explicitly);
  `servo.Route("POST /p")` must exactly match a served route (method + pattern) — both validated
  at generate time.
- `T` resolves from the graph like any dependency (own constructor, own deps, own lifecycle,
  Override-able). Missing/mis-shaped `Middleware` method, or a scoped `T`: diagnostics.

### Extractors (Kind B — typed per-request handler parameters)

An extractor is a graph node with a by-name method (per-type signature, like `ScopeKey`):

```go
func (e *UserExtractor) Extract(r *http.Request) (*auth.User, error)
```

declared top-level in `Build`:

```go
servo.Extract[*mw.UserExtractor](),
```

- The method's result type `V` makes **every handler parameter of type `V`** (after `ctx` and the
  request struct) a per-request extracted value instead of a graph dependency.
- The emitted adapter calls `Extract(r)` after request decoding and before the handler; an
  extraction error short-circuits through the same status contract (4xx message to the client,
  5xx canonical + logged), so `servo.Status.UNAUTHORIZED.Wrap(err)` is a 401 before the handler
  runs. Extractors run after wrap middleware, so they see context values middleware planted.
- Hard diagnostics: two extractors producing the same `V`; `V` also having a graph provider
  ("a type is constructed once or extracted per request, never both"); missing/mis-shaped
  `Extract` method; scoped extractor type.

### Markers summary (all panic at runtime; parsed as syntax)

```go
type HTTPOption struct{}

func HTTP(...HTTPOption) Marker          // was HTTP(); still unreleased, free to change
func Group(name string) HTTPOption       // inside HTTP: declare; inside Use: select
func Use[T any](...HTTPOption) HTTPOption
func Route(pattern string) HTTPOption    // only inside Use; "METHOD /pattern", constant
func Extract[T any]() Marker
```

String arguments must be constant expressions (the spec file is read, never run) — same rule as
`Linger`/`Max`.

### Emitted shape

One server type per served group (`httpServer`, `httpTelemetryServer`, …), each an App field with
the stop triple. `Run` joins every group server (plus node Runners) in one errgroup. `Ready`
reports one entry per group: `"http"`, `"http:telemetry"`. `Shutdown` stops all group servers
first (the inbound edges), then nodes in reverse order. Group constructors return `(T, error)` so
a missing `Groups` map entry fails `New` with rollback. The repeated per-adapter error mapping is
extracted into one emitted `writeFailure`-style method per server, shared by extractor and handler
error paths (the 2xx success-as-error branch stays in the adapter, where `res` is in scope).

## Non-goals (this slice)

- Opting an injector out of the default group.
- Response middleware with typed access to the response body.
- Per-group `servo.Use` differences across injectors beyond what group selection gives.
- gRPC or any second transport.
