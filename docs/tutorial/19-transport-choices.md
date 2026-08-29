# 19. Transport choices

Everything so far has had exactly one way in: the `net/http` server from
[chapter 10](10-api-layer.md). That was a choice, not a requirement, and this chapter shows what it
costs to make a different one — first Gin, then gRPC serving alongside REST on a single port.

The interesting part isn't either framework. It's how little moves. The service layer, the
repository, the cache, the broker, the session scope and every test below the transport are
untouched by both variants. What changes is one package and one spec file.

That is worth demonstrating rather than asserting, so all three live in the repository at once:

| Transport | Package | Injector | Binary |
| --- | --- | --- | --- |
| `net/http` | `api/` | `cmd/orders` | `orders` |
| Gin | `ginapi/` | `cmd/ordersgin` | `ordersgin` |
| gRPC + REST | `grpcapi/` | `cmd/ordersgrpc` | `ordersgrpc` |

Each is a separate `servo.Build`, so each gets its own `servo_gen.go`. Pick one for a real service;
having three is a property of this being a tutorial.

## What every variant shares

Three things are the same in all of them, and two of those are worth stating as rules rather than
as details.

**The operational endpoints are never on the public port.** `/healthz`, `/readyz` and `/metrics`
are served by `admin.New` on `ADMIN_ADDR`, and nothing else is. This is the one boundary in the
service that is enforced by tests rather than by convention — see
[Keeping the admin port private](#keeping-the-admin-port-private) below.

**The API contract is public, and embedded.** `openapi.Handler()` serves `/openapi.yaml` and a
Swagger UI at `/swagger/` on the public listener, beside the API it documents.

**The middleware order is the same**, because the reasoning behind it from
[chapter 14](14-resilience.md) doesn't change with the framework.

## Gin

`ginapi/` is a straight port. Same routes, same JSON, same status codes — `ginapi/server.go`:

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
middleware, which write their own text format straight to stdout. This service already emits
structured JSON through the injected logger ([chapter 13](13-observability.md)), and a second
format interleaved with the first makes both harder to consume. `ginapi/middleware.go` reimplements
both against `*observability.Logger`.

**`gin.SetMode(gin.ReleaseMode)` is explicit.** Left alone, Gin picks its mode from `GIN_MODE` and
prints a startup banner plus a route dump when that variable is unset. Deciding it in code means the
output doesn't depend on an environment variable nothing else in the service reads.

**The route group is the real difference.** The `net/http` version wraps each protected handler
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
quantity. That rule lives in `domain` ([chapter 4](04-domain-layer.md)) and duplicating it here
would mean two places to change it, one of which the tests below the transport never exercise.

The middleware chain keeps the layering from chapter 14, with Gin as the innermost handler:

```go
var handler http.Handler = r
handler = metrics.Middleware(handler)
handler = tracer.Middleware(handler)
handler = limiter.Middleware(handler)
```

Those three stay `net/http` middleware rather than becoming `gin.HandlerFunc`. They are
transport-agnostic and shared verbatim with the `api` variant; wrapping them would mean maintaining
two copies of each for no behavioural gain.

### Running it

```
$ make run-gin
```

Same environment variables, same port, same responses as `cmd/orders`. That is the claim, and
`cmd/ordersgin/app_test.go` checks the parts of it that matter.

## gRPC, on the same port as REST

`grpcapi/` is the advanced one, and it does something the other two don't: it serves gRPC and HTTP
from a single listener.

You do not have to. Two listeners on two ports is simpler and perfectly normal. One port buys you
one firewall rule, one TLS certificate, one address for a client to know, and one thing to
configure in a service mesh — which is worth something in an environment where each of those is a
ticket. It costs you a dispatch function that has to be exactly right.

### The contract comes first

With gRPC the wire format is generated from a schema, not written by hand. `grpcapi/ordersv1/orders.proto`:

```proto
service Orders {
  rpc Login(LoginRequest) returns (LoginResponse);
  rpc CreateOrder(CreateOrderRequest) returns (Order);
  rpc GetOrder(GetOrderRequest) returns (Order);
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
  rpc Recent(RecentRequest) returns (RecentResponse);
}
```

The version is in the package name — `package ordersv1` — rather than in a URL path, because a gRPC
method's full name (`/ordersv1.Orders/GetOrder`) *is* its wire identity. Renaming the package breaks
every client, and it should look like it breaks every client.

The generated `.pb.go` files are committed, exactly like the `gomock` mocks from
[chapter 8](08-service-layer.md), so cloning this repository and running `go test ./...` doesn't
first require installing `protoc` and two plugins. Regenerate with:

```
$ go generate ./grpcapi/...
```

That runs `protoc` and then `gofumpt`. The second step is not decoration: `protoc-gen-go` emits one
alphabetical import block mixing the standard library with module paths, which `gofmt` accepts and
every import organizer rewrites — so without it, a freshly generated file and the committed one
differ, and the repository's own format check fails on a file nobody edited.

### How one port carries two protocols

gRPC is HTTP/2 with a content type of `application/grpc`. A handler that checks both facts can route
each request to either the gRPC server or an ordinary mux — `grpcapi/server.go`:

```go
func dispatch(grpcServer *grpc.Server, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}
```

Both halves of that condition are load-bearing. `ProtoMajor == 2` alone would capture any HTTP/2
request, including a browser fetching `/swagger/` over an HTTP/2 connection. The content type alone
would be trivially spoofable over HTTP/1.1, handing the gRPC server a connection it cannot speak on.

Then the listener has to actually offer HTTP/2 without TLS, which a plain `http.Server` does not:

```go
protocols := new(http.Protocols)
protocols.SetHTTP1(true)
protocols.SetUnencryptedHTTP2(true)

s.http = &http.Server{
	Addr:      cfg.HTTPAddr,
	Handler:   dispatch(s.grpc, rest),
	Protocols: protocols,
}
```

If you have seen this done before, you have probably seen `golang.org/x/net/http2/h2c`, which
achieved the same thing by hijacking the connection. It is deprecated now that the standard library
supports unencrypted HTTP/2 directly. Behind a TLS-terminating proxy or with real certificates none
of this is needed at all — ALPN negotiates `h2` and both protocols arrive over the same connection
for free. A tutorial that required you to generate certificates before it would run would be a
worse tutorial.

### Only the REST half gets the HTTP middleware

```go
var rest http.Handler = mux
rest = metrics.Middleware(rest)
rest = tracer.Middleware(rest)
rest = limiter.Middleware(rest)
```

Note that `rest` is wrapped, and the dispatch is not. There are two reasons, and the second one will
cost you an afternoon if you get it wrong.

The first is that the numbers would be meaningless. Those middlewares record HTTP methods, paths and
status codes; a gRPC call has a method name and a status *code*, not a path and a 404. gRPC has its
own equivalents — the interceptor below, and `grpc.StatsHandler` for metrics and tracing.

The second is mechanical. Each of those middlewares wraps the `ResponseWriter` to capture the status
code, and gRPC needs the original one: it writes trailers and flushes frames through interfaces a
wrapper doesn't carry. Put any of them outside the dispatch and every RPC fails with a protocol
error and **no server-side log line at all**, because the request never reaches a handler that logs.
The client sees `frame too large, note that the frame header looked like an HTTP/1.1 header` and the
server appears to be fine.

### Authentication is an interceptor

The third shape for the same idea. `net/http` wraps each handler, Gin uses a route group, gRPC uses
a unary interceptor that sees every call and decides per method — `grpcapi/auth.go`:

```go
func authInterceptor(issuer *auth.Issuer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if info.FullMethod == "/ordersv1.Orders/Login" {
			return handler(ctx, req)
		}
		// ... read "authorization" from metadata, verify, then:
		ctx = context.WithValue(ctx, claimsKey, claims)
		ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
		return handler(ctx, req)
	}
}
```

`Login` is exempt because it is how a caller gets a token. Listing the exemption by full method name
means adding an unauthenticated method is a deliberate edit here — the opposite default from
"register a route and hope someone remembers the wrapper".

The last two lines are the same two lines as the HTTP variants, for the same reason: the claims are
what handlers read, and the session key is what servo's generated accessor reads
([chapter 12](12-scoped-instances.md)). The scope has nothing to do with the transport, which is why
`GetOrder` records a view identically in all three.

The error mapping changes vocabulary but not structure — `codes.NotFound` where HTTP said 404,
`codes.PermissionDenied` where it said 403. That mapping lives in the transport in every variant,
which is exactly why `domain` names neither.

### Talking to it

```
$ make run-grpc
```

REST on that port, unchanged:

```
$ curl -s localhost:8080/openapi.yaml | head -3
```

and gRPC on the same one. `grpcurl` has to be pointed at the `.proto`, because this server does not
register the reflection service — a production binary enumerating its own API to anonymous callers
is a decision, not a default, and registering it is one line if you want it:

```
$ grpcurl -plaintext -import-path grpcapi/ordersv1 -proto orders.proto \
    -d '{"username":"alice","password":"password123"}' \
    localhost:8080 ordersv1.Orders/Login
```

`-import-path` is the directory imports resolve against, and `-proto` is the file relative to it.
The service's own tests reach the same endpoint without any of this, over a real TCP connection —
see `grpcapi/grpcapi_test.go`, which is the executable version of the claim that one port carries
both protocols.

## Keeping the admin port private

All three variants serve `/healthz`, `/readyz` and `/metrics` from `admin.New` on `ADMIN_ADDR`, and
never from the public listener. The reason is worth being explicit about: the health endpoints
enumerate every component in the graph by name along with its status, and `/metrics` exposes request
rates, latencies and error counts per route. Together they describe the shape and health of the
system precisely enough to be worth hiding.

That's a boundary, and boundaries that exist only in someone's memory erode. Each variant asserts it:

```go
for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
	resp, err := http.Get(ts.URL + path)
	...
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s = %d on the public listener, want 404", path, resp.StatusCode)
	}
}
```

Adding `/metrics` to a public router — the kind of thing that looks harmless in review — fails that
test in every variant.

The admin server is a package rather than a copy in each `main.go` for the same reason: three copies
of a security boundary is three chances to get one wrong. It takes an interface rather than a
concrete `*App`, which is what lets one implementation serve three injectors that each generate
their own `App` type:

```go
type Checker interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}
```

### Why Swagger is on the *public* port

`/openapi.yaml` and `/swagger/` are served on the public listener, beside the API they describe.
That's a deliberate split from the three above: the contract tells a caller how to use endpoints
they are already allowed to reach, while the health and metrics endpoints describe the service's
internals.

It is a defensible default and not the only one. Publishing the UI publishes your full endpoint
list, DTO shapes and auth scheme, which is fine for a public API and not fine for an internal one
whose surface is itself sensitive. `openapi.Handler()` is an ordinary `http.Handler`, so moving it is
one line — mount it in `admin.New` instead, and consumers fetch the spec from inside the cluster or
get it handed to them out of band.

One caveat worth knowing: the UI loads Swagger UI's JavaScript from a CDN rather than vendoring
several megabytes into the repository. A browser with no route to the internet gets an empty page,
and the spec itself is still readable at `/openapi.yaml`. A service that must document itself in an
air-gapped network should vendor the assets and serve them from `openapi/`.

## What this chapter actually showed

Three transports, one service layer, and nothing below the transport aware that there is more than
one. `diff` the three spec files and you get the root declaration and the import that serves it,
and nothing else:

```go
servo.Root[*api.Server](),      // cmd/orders
servo.Root[*ginapi.Server](),   // cmd/ordersgin
servo.Root[*grpcapi.Server](),  // cmd/ordersgrpc
```

Everything else in all three `servo.Build` calls is identical — the same binds, the same session
scope, the same overrides for testing. servo resolves each into its own graph and each `App` gets
the components its transport needs, which is the practical version of the claim
[chapter 1](01-architecture-overview.md) opened with: the layers are separable, and you find out
whether they really are by trying to separate them.

## Next

[Chapter 20: Alternatives and further reading](20-alternatives-and-further-reading.md) — the choices
this tutorial made at every layer, what the real alternatives were, and when you'd actually want
them instead.
