# 13. The transport you don’t write

Chapter 10 wrote the HTTP edge by hand: a `Server` struct, a mux, a middleware chain built in
exactly the right order, handlers that decode, call, encode, and one `writeDomainError` where
domain sentinels become status codes. Chapters 11 and 12 rebuilt that edge twice — same API,
different router, different protocol — and the point both times was the price tag: the edge is
the part you rewrite when the transport changes, and the service layer never notices.

This chapter deletes the edge. The routes become comments, the server becomes generated code, and
what remains in the repository is exactly the part that was ever specific to this service: the
request and response shapes, the handlers, the auth decision, and the one place a domain error
becomes a status. Everything else — the mux, the decoding, the encoding, the listener, the
shutdown — comes out of `servo generate`, the same command that already writes `New` and
`Shutdown`.

The full mechanics of the markers this chapter drops into the spec file are the next chapter's
job; here we use them the way chapters 11 and 12 used `servo.Root` — as two lines to accept on
faith for twenty minutes.

## The shapes, now exported

The package is `internal/transport/servoapi`, and its DTOs are the api package's with one visible
difference — they are exported, because the generated adapters live in the injector package and
construct the request structs by name. The other difference is what the tags mean: they are not
just serialization hints any more, they are the routing contract.

```go
type CreateOrderReq struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

type GetOrderReq struct {
	ID string `path:"id"`
}

type ListOrdersReq struct {
	Limit  int `query:"limit"`
	Offset int `query:"offset"`
}
```

`path:"id"` binds `{id}` from the route pattern. `query:"limit"` binds `?limit=` with generated
`strconv` parsing — a caller sending `?limit=abc` gets a 400 naming the parameter, and the
handler never runs. Fields with `json` tags decode from the body, for the methods that carry one.
The [binding reference](../reference/http.md) has the full table.

## The handlers

A handler is an exported function with a directive comment. First parameter `context.Context`,
then the request struct, then whatever the graph can provide:

```go
//servo:post /auth/login
func Login(ctx context.Context, req *LoginReq, authSvc *service.AuthService) (servo.Json[*LoginResp], error) {
	token, err := authSvc.Login(ctx, req.Username, req.Password)
	if err != nil {
		return nil, statusFor(err)
	}
	return servo.JSON(&LoginResp{Token: token}), nil
}

//servo:post /orders
func CreateOrder(ctx context.Context, req *CreateOrderReq, claims auth.Claims, orders *service.OrderService) (servo.Json[*OrderResp], error) {
	order, err := orders.CreateOrder(ctx, claims.UserID, req.Item, req.Quantity)
	if err != nil {
		return nil, statusFor(err)
	}
	return servo.JSON(NewOrderResp(order)), servo.Status.CREATED
}
```

Read `CreateOrder`'s signature slowly, because all four parameter kinds are in it. `ctx` is the
request's context. `req` was decoded from the body — the handler never sees `*http.Request`.
`orders` is the same singleton every other transport injected. And `claims` is something new: a
**per-request extracted value**, which we will come back to with the auth middleware below.

The return works both directions. A `nil` error is a 200 with the JSON body;
`servo.Status.CREATED` in the error position picks 201 — chapter 10's handlers called
`writeJSON(w, http.StatusCreated, …)` for the same effect. And `writeDomainError` survives almost
unchanged as `statusFor`:

```go
func statusFor(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return servo.Status.NOT_FOUND.New("not found")
	case errors.Is(err, domain.ErrForbidden):
		return servo.Status.FORBIDDEN.New("access forbidden")
	...
```

Same single point, same sentinels, same discipline — everything below the API layer still deals
only in domain errors. The difference is that it *returns* a status-carrying error for the
generated adapter to write, instead of writing the response itself. 4xx messages reach the
client; a 500's wrapped detail goes to the log and the client sees only canonical text — the rule
chapter 10 enforced by hand, now enforced by generated code.

## The session survives, and so does its rule

`GetOrder` still records what the caller looked at:

```go
//servo:get /orders/{id}
func GetOrder(ctx context.Context, req *GetOrderReq, claims auth.Claims, orders *service.OrderService, sessions session.Sessions) (servo.Json[*OrderResp], error) {
	...
	if sess, release, err := sessions.Acquire(ctx); err == nil {
		defer release()
		sess.RecordView(order.ID)
	}
	...
```

The parameter is `session.Sessions` — the accessor interface from chapter 15 — not
`*session.Session`. That is not a style choice. A handler's dependencies are resolved once, when
the server is constructed; a `*session.Session` parameter would pin one user's session for the
life of the process, and `servo generate` refuses to emit it, with the same widening diagnostic
the spec chapter shows for singletons. Acquire per request, release per request: the transport
changed, the scope rule didn't.

## Auth: one middleware, one extractor

Chapter 10's `requireAuth` did three jobs in one wrapper: verify the Bearer token, hand the
claims to handlers, and plant the session key in the context. Those jobs split cleanly across the
two seams the generated server offers.

The verification and the planting stay together, as a **middleware** — an ordinary graph node
with one method:

```go
func (m *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		...
		claims, err := m.issuer.Verify(token)
		...
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

Handing claims to handlers becomes an **extractor** — the thing that made `claims auth.Claims` a
legal parameter above:

```go
func (e *ClaimsExtractor) Extract(r *http.Request) (auth.Claims, error) {
	claims, ok := r.Context().Value(claimsKey).(auth.Claims)
	if !ok {
		return auth.Claims{}, servo.Status.UNAUTHORIZED.New("missing bearer token")
	}
	return claims, nil
}
```

Once the spec declares it, every handler parameter of type `auth.Claims` is produced per request
by this method — reading what `Auth` planted, so the token is verified exactly once. A failed
extraction short-circuits through the same status mapping as a handler error: 401, before the
handler runs. `Login` takes no `auth.Claims`, so the extractor never runs for it.

## The chain, declared instead of built

Here is chapter 10's middleware assembly:

```go
handler := loggingMiddleware(log, mux)
handler = metrics.Middleware(handler)
handler = tracer.Middleware(handler)
handler = limiter.Middleware(handler)
handler = recoverMiddleware(log, handler)
```

And here is this chapter's:

```go
servo.HTTP(
	servo.Use[*middleware.Recover](),
	servo.Use[*resilience.RateLimiter](),
	servo.Use[*observability.Tracer](),
	servo.Use[*observability.Metrics](),
	servo.Use[*middleware.AccessLog](),
	servo.Use[*servoapi.Auth](
		servo.Route("POST /orders"),
		servo.Route("GET /orders/{id}"),
		servo.Route("GET /orders"),
		servo.Route("GET /me/recent"),
	),
),
servo.Extract[*servoapi.ClaimsExtractor](),
```

Three things to notice. `Tracer`, `Metrics` and `RateLimiter` attach **unchanged** — they always
had the `Middleware(next http.Handler) http.Handler` method, and that method is the whole
contract; nothing in `internal/observability` or `internal/resilience` knows this transport
exists. The hand-written `recoverMiddleware` and `loggingMiddleware` are replaced by servo's
shipped [`middleware`](../reference/middleware.md) package, because they carried no
service-specific logic worth keeping. And declaration order **is** wrap order, outermost first —
the same order chapter 10 built by hand, with the same reasoning (recover must see every panic;
metrics must sit against the mux), except now a misordering is a visible diff in the spec file
rather than a subtle bug in a constructor.

`Auth` uses `servo.Route` selectors, the declarative form of wrapping four routes and not the
fifth. If a selector names a route that doesn't exist — a typo, a renamed pattern —
`servo generate` fails and says so.

The listener address comes from a `*servo.HTTPConfig` provider reading the same `HTTP_ADDR` every
deployment already sets. servo never constructs config; you do — chapter 3's rule, unchanged.

## What got generated

Run `make generate` and read `cmd/ordersservo/servo_gen.go` next to chapter 10's `server.go`.
The file's header now documents the routing table alongside the graph. Each route got an adapter
— here is the interesting middle of `handleCreateOrder`:

```go
req := &servoapi.CreateOrderReq{}
r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
if err := json.NewDecoder(r.Body).Decode(req); err != nil {
	httpWriteError(w, http.StatusBadRequest, "malformed request body")
	return
}
claims, err := s.app.claimsExtractor.Extract(r)
if err != nil {
	httpWriteFailure(w, "servoapi.CreateOrder", "POST /orders", err)
	return
}
res, err := servoapi.CreateOrder(r.Context(), req, claims, s.app.orderService)
```

That is `handlers.go`'s boilerplate from chapter 10, emitted instead of maintained. The
registration probe already ran at generate time: two routes that would make `http.ServeMux` panic
at startup are a build failure with net/http's own message.

## Testing without a port

Chapter 10 exposed `Handler()` on the hand-written server precisely so tests could drive the
routed, middleware-wrapped stack through `httptest` without binding a listener. The generated
server exposes the same seam:

```go
app, err := NewTestApp(context.Background())
...
h := app.HTTPHandler("")

w := httptest.NewRecorder()
h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body)))
```

`cmd/ordersservo/app_test.go` runs the full flow — login, an unauthenticated 401, a 201 create
through the mocks, and `/me/recent` answered by the scoped session alone — with none of Postgres,
Redis or NATS running, exactly like `cmd/orders`'s test. One wrinkle worth reading in that file:
`Acquire` refuses a context that cannot be cancelled, so the session-touching request wraps
`httptest.NewRequest`'s context in a cancellable one, which is what any real server provides
anyway.

Two commands worth running while it's fresh: `servo list` now ends with the module's routing
table, and `servo graph --format=json` carries the routes, attachments and extractors under an
`"http"` key.

## What the swap actually cost

The same honest accounting chapters 11 and 12 ended with:

**Deleted:** `server.go`, the mux and its ordering comment, `middleware.go`'s `requireAuth`
plumbing and `recoverMiddleware`/`loggingMiddleware`, all of `handlers.go`'s decode/encode
boilerplate, `writeJSON`/`writeError`/`writeDomainError`-as-a-writer. What's left in `servoapi`
is roughly a third of the api package, and none of it touches `net/http` except the middleware
and the extractor — the two places that genuinely are about HTTP.

**Gained:** generate-time failure for route conflicts, unresolvable handler dependencies, a
scoped type where a singleton belongs, a middleware selector naming nothing, and a typo'd
directive; one consistent error shape; the test seam for free; and a routing table you can ask
the CLI for.

**Given up:** the raw `http.ResponseWriter`. There is no streaming-per-event SSE shape, no
content negotiation, no per-handler header manipulation beyond what the response family carries —
`servo.Json`, `servo.Xml`, `servo.Text`, `servo.Blob`, `servo.Redirect` and friends are the menu.
The admin listener from chapter 10 also stays hand-wired (or on its own group): `Health` and
`Ready` are methods on the App, and how they are served remains your decision. If a handler needs
the writer itself, that route belongs in a hand-written transport — the four injectors in this
module prove the two styles coexist in one service without either knowing about the other.

Next: [Chapter 14 — Wiring with servo](14-wiring-with-servo.md), where the spec file this chapter
asked you to take on faith gets explained line by line.
