# 12. gRPC as the transport

[Chapter 10](10-api-layer.md) built the API layer on `net/http`, and
[chapter 11](11-gin-transport.md) rebuilt it in Gin. This chapter serves the same service layer
over gRPC — and serves gRPC and REST from a **single port**, which is the part worth reading even
if you already know gRPC.

It assumes chapter 10: the DTOs, the domain-error mapping and the session scope are shared rather
than repeated here. As in chapter 11, the spec-file change at the end belongs to
[chapter 13](13-wiring-with-servo.md), so come back to it if you are reading in order. The working
code is
[`examples/tutorial/grpcapi`](https://github.com/okian/servo/tree/master/examples/tutorial/grpcapi),
wired by `cmd/ordersgrpc`.

`grpcapi/` is the advanced option, and it does something the other two don't: it serves gRPC and
HTTP from a single listener.

You do not have to. Two listeners on two ports is simpler and perfectly normal. One port buys you
one firewall rule, one TLS certificate, one address for a client to know, and one thing to
configure in a service mesh — worth something where each of those is a ticket. It costs you a
dispatch function that has to be exactly right.

## The contract comes first

With gRPC the wire format is generated from a schema, not written by hand.
`grpcapi/ordersv1/orders.proto`:

```proto
service Orders {
  rpc Login(LoginRequest) returns (LoginResponse);
  rpc CreateOrder(CreateOrderRequest) returns (Order);
  rpc GetOrder(GetOrderRequest) returns (Order);
  rpc ListOrders(ListOrdersRequest) returns (ListOrdersResponse);
  rpc Recent(RecentRequest) returns (RecentResponse);
}
```

The version is in the package name — `package ordersv1` — rather than in a URL path, because a
gRPC method's full name (`/ordersv1.Orders/GetOrder`) *is* its wire identity. Renaming the package
breaks every client, and it should look like it breaks every client.

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

## How one port carries two protocols

gRPC is HTTP/2 with a content type of `application/grpc`. A handler that checks both facts can
route each request to either the gRPC server or an ordinary mux — `grpcapi/server.go`:

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
would be trivially spoofable over HTTP/1.1, handing the gRPC server a connection it cannot speak
on.

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
of this is needed at all — ALPN negotiates `h2` and both protocols arrive over one connection for
free. A tutorial that required you to generate certificates before it would run would be a worse
tutorial.

## Only the REST half gets the HTTP middleware

```go
var rest http.Handler = mux
rest = metrics.Middleware(rest)
rest = tracer.Middleware(rest)
rest = limiter.Middleware(rest)
```

Note that `rest` is wrapped and the dispatch is not. Two reasons, and the second will cost you an
afternoon if you get it wrong.

The first is that the numbers would be meaningless. Those middlewares record HTTP methods, paths
and status codes; a gRPC call has a method name and a status *code*, not a path and a 404. gRPC has
its own equivalents — the interceptor below, and `grpc.StatsHandler` for metrics and tracing.

The second is mechanical. Each of those middlewares wraps the `ResponseWriter` to capture the
status code, and gRPC needs the original one: it writes trailers and flushes frames through
interfaces a wrapper doesn't carry. Put any of them outside the dispatch and every RPC fails with a
protocol error and **no server-side log line at all**, because the request never reaches a handler
that logs. The client sees `frame too large, note that the frame header looked like an HTTP/1.1
header` and the server appears to be fine.

## Authentication is an interceptor

The third shape for the same idea. `net/http` wraps each handler,
[Gin](11-gin-transport.md) uses a route group, and gRPC uses a unary interceptor that sees every call
and decides per method — `grpcapi/auth.go`:

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

`Login` is exempt because it is how a caller gets a token. Listing the exemption by full method
name means adding an unauthenticated method is a deliberate edit here — the opposite default from
"register a route and hope someone remembers the wrapper".

The last two lines are the same two lines as the HTTP transports, for the same reason: the claims
are what handlers read, and the session key is what servo's generated accessor reads
([chapter 14](14-scoped-instances.md)). The scope has nothing to do with the transport, which is
why `GetOrder` records a view identically in all three.

The error mapping changes vocabulary but not structure — `codes.NotFound` where HTTP said 404,
`codes.PermissionDenied` where it said 403. That mapping lives in the transport in every case,
which is exactly why `domain` names neither.

## Integrating with servo

Again one line — `cmd/ordersgrpc/spec.go`:

```go
servo.Build(
	servo.Root[*grpcapi.Server](),
	// ... the same binds, scope and overrides
)
```

servo neither knows nor cares that this server speaks two protocols. It sees a constructor with
dependencies and a `Run`/`Stop` pair, which is all a root has to be.

## Talking to it

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

## See also

- [Chapter 11: Gin as the transport](11-gin-transport.md) — the same API in Gin, if you want a
  router rather than a second protocol.
- [Chapter 10: API layer](10-api-layer.md) — the `net/http` version, and the DTOs and
  error mapping all three share.
