# Gin as the transport

[Chapter 10](../tutorial/10-api-layer.md) builds the API layer on the standard library's
`net/http`. This page is the same API in [Gin](https://github.com/gin-gonic/gin) — same routes,
same JSON, same status codes — and exists so you can see exactly what changing HTTP frameworks
costs.

The answer is: one package and one line in the spec file. The service layer, the repository, the
cache, the broker, the session scope and every test below the transport are untouched. Read
chapter 10 first — the DTOs and the domain-error mapping are shared, and this page assumes them.

The working code is [`examples/tutorial/ginapi`](https://github.com/okian/servo/tree/master/examples/tutorial/ginapi),
wired by `cmd/ordersgin`.

## The router

Same routes, same JSON, same status codes as chapter 10 — `ginapi/server.go`:

```go
gin.SetMode(gin.ReleaseMode)

r := gin.New()
r.Use(recoverMiddleware(log), loggingMiddleware(log))

r.Any("/openapi.yaml", gin.WrapH(openapi.Handler()))
r.Any("/swagger/*any", gin.WrapH(openapi.Handler()))

r.POST("/auth/login", s.handleLogin)

authed := r.Group("/", requireAuth(issuer))
authed.POST("/orders", s.handleCreateOrder)
authed.GET("/orders/:id", s.handleGetOrder)
authed.GET("/orders", s.handleListOrders)
authed.GET("/me/recent", s.handleRecent)
```

Four things in there are decisions, not boilerplate.

**`gin.New()`, not `gin.Default()`.** `Default` installs Gin's own `Logger` and `Recovery`
middleware, which write their own text format straight to stdout. This service emits structured
JSON through the injected logger ([chapter 13](../tutorial/13-observability.md)), and a second format
interleaved with the first makes both harder to consume. `ginapi/middleware.go` reimplements both
against `*observability.Logger`.

**`gin.SetMode(gin.ReleaseMode)` is explicit.** Left alone, Gin picks its mode from `GIN_MODE` and
prints a startup banner plus a route dump when that variable is unset. Deciding it in code means
the output doesn't depend on an environment variable nothing else in the service reads.

**The route group is the real difference** from `net/http`, which wraps each protected handler
individually:

```go
mux.HandleFunc("POST /orders", requireAuth(issuer, s.handleCreateOrder))
mux.HandleFunc("GET /orders/{id}", requireAuth(issuer, s.handleGetOrder))
```

Forgetting that wrapper on one route publishes it. With a group, authentication is a property of
where the route is registered, and a new route inside `authed` cannot miss it. That is a small
structural advantage and it is most of why people reach for a router.

**Binding tags move validation earlier.** `ginapi/dto.go` declares:

```go
type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}
```

so `c.ShouldBindJSON` rejects an empty username before the handler runs. Note what is *not* tagged:
`createOrderRequest.Quantity` has no `binding:"min=1"`, even though the domain requires a positive
quantity. That rule lives in `domain` ([chapter 4](../tutorial/04-domain-layer.md)) and duplicating it here
would mean two places to change it, one of which the tests below the transport never exercise.

The middleware chain keeps chapter 10's layering, with Gin as the innermost handler:

```go
var handler http.Handler = r
handler = metrics.Middleware(handler)
handler = tracer.Middleware(handler)
handler = limiter.Middleware(handler)
```

Those three stay `net/http` middleware rather than becoming `gin.HandlerFunc`. They are
transport-agnostic and shared verbatim with `api/`; wrapping them would mean maintaining two copies
of each for no behavioural gain.

## Integrating with servo

One line different from `cmd/orders` — `cmd/ordersgin/spec.go`:

```go
servo.Build(
	servo.Root[*ginapi.Server](),
	// ... the same binds, scope and overrides
)
```

`diff` the two spec files and you get that root declaration and the import that serves it, and
nothing else. `ginapi.Server` has the same `Run`/`Stop` shape, so servo treats it identically.

```
$ make run-gin
```

## See also

- [gRPC as the transport](transport-grpc.md) — the same API again, over gRPC, sharing one port
  with REST.
- [Chapter 10: API layer](../tutorial/10-api-layer.md) — the `net/http` version, and the DTOs and
  error mapping all three share.
