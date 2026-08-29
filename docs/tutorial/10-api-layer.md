# 10. API layer

Everything up to this chapter has been assembled but never actually reachable from outside the
process. This chapter wires it to a real transport: routes, request and response shapes, and the
middleware that runs on every request. By the end, `curl` will be able to log in, create an order,
and read it back — for real, against everything built in chapters 5 through 9.

This chapter uses the standard library's `net/http`, which needs no dependency and is enough for
everything here. If you would rather use a router, or need gRPC, the next two chapters implement
the identical API without touching anything below the transport:

- [**Chapter 11: Gin as the transport**](11-gin-transport.md) — route groups and binding-tag
  validation instead of per-handler wrappers.
- [**Chapter 12: gRPC as the transport**](12-grpc-transport.md) — and serving gRPC and REST from a
  single port.

Both are optional: the service is complete with `net/http` alone, and
[chapter 13](13-wiring-with-servo.md) follows on from this one whether you read them or not. The
request shapes, the domain-error mapping and the middleware reasoning are the same in all three,
and both chapters assume this one.

## Define the request and response shapes first

The API's JSON shapes are their own thing, separate from `domain.Order` — a domain type can gain a
field for internal reasons without that automatically becoming part of the public API contract.
Create `api/dto.go`:

```go
package api

import (
	"time"
	"uuid"

	"example.com/servoorders/internal/domain"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

type createOrderRequest struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

type orderResponse struct {
	ID        uuid.UUID `json:"id"`
	Item      string    `json:"item"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func newOrderResponse(o *domain.Order) orderResponse {
	return orderResponse{
		ID:        o.ID,
		Item:      o.Item,
		Quantity:  o.Quantity,
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	}
}

type listOrdersResponse struct {
	Orders []orderResponse `json:"orders"`
}

type errorResponse struct {
	Error string `json:"error"`
}
```

Every one of these types is unexported — nothing outside this package needs to construct a
`loginRequest` directly, and keeping them private means changing the wire format later can't
accidentally become a breaking change for Go code that imported them.

## One place turns a domain error into a status

The transport is where a `domain` sentinel error becomes a status the caller understands, and
**nothing below it knows what a status is**. That is why [chapter 4](04-domain-layer.md) defines
`ErrNotFound` and `ErrForbidden` rather than constants named after HTTP codes: the service layer
would otherwise have to know it is being called over HTTP — which, on the
[gRPC transport](12-grpc-transport.md), it is not.

| Domain error | HTTP status | gRPC code |
| --- | --- | --- |
| `ErrNotFound` | `404` | `codes.NotFound` |
| `ErrForbidden` | `403` | `codes.PermissionDenied` |
| `ErrInvalidCredentials` | `401` | `codes.Unauthenticated` |
| `ErrValidation` | `400` | `codes.InvalidArgument` |
| anything else | `500` | `codes.Internal` |

`writeDomainError` below is this chapter's implementation. The other two transports have the same
`switch` under a different name, and that is the only thing about error handling that changes
between them.

## Build the middleware chain

Two cross-cutting concerns apply to (almost) every request: authentication, and not letting a
panic in one handler take down every other in-flight request. Create `api/middleware.go`. Start
with how a verified request identifies its caller — a `context.Context` key, and the two functions
that use it:

```go
package api

import (
	"context"
	"net/http"
	"strings"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/observability"
)

type contextKey int

const claimsKey contextKey = 0

func requireAuth(issuer *auth.Issuer, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		claims, err := issuer.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next(w, r.WithContext(ctx))
	}
}

func claimsFromContext(ctx context.Context) (auth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(auth.Claims)
	return claims, ok
}
```

`requireAuth` wraps a single handler (it's called per-route below, not registered globally) — the
login endpoint has no token to check yet, so it can't go through this. Notice this is the only
place that knows about the `Authorization` header or the 401 response shape; `auth.Verify` itself,
from [chapter 9](09-authentication.md), never heard of HTTP. That split is deliberate: the JWT
logic stays reusable from a transport other than HTTP without any change.

The other two middlewares apply to every request, so they wrap the whole handler rather than one
route at a time:

```go
func recoverMiddleware(log *observability.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(r.Context(), "api: panic recovered", "panic", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(log *observability.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
```

`statusWriter` exists because `http.ResponseWriter` doesn't expose what status code a handler
already wrote — wrapping it is the standard way to capture that for logging. This logging is
intentionally bare-bones; [chapter 15](15-observability.md) replaces it with something that
correlates each line to a trace, using the same wrapper.

## Write the handlers

Create `api/handlers.go`. `handleLogin` is the simplest one — decode, delegate to
`service.AuthService`, map whatever comes back:

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"uuid"

	"example.com/servoorders/internal/domain"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	token, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{Token: token})
}
```

`writeDomainError` is worth building next, since every other handler uses it too — this is the one
place in the whole service that turns a `domain` sentinel error into an HTTP status code:

```go
func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, domain.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
```

Every handler below this point only ever returns a `domain` error upward — never an
`http.StatusCode` of its own — which is exactly the boundary [chapter 4](04-domain-layer.md)
described before either side existed. The three order handlers follow: create, get (with the
authorization check happening on whatever the service layer actually returns, not before), and
list:

```go
func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context()) // requireAuth guarantees this is present

	var req createOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}

	order, err := s.orders.CreateOrder(r.Context(), claims.UserID, req.Item, req.Quantity)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newOrderResponse(order))
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context())

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	order, err := s.orders.GetOrder(r.Context(), claims.UserID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newOrderResponse(order))
}
```

Notice what `handleGetOrder` does *not* do: it never checks whether `order.UserID` matches
`claims.UserID` itself. That check lives in `authorize` back in [chapter 8](08-service-layer.md),
one layer down — the API layer's job stops at translating whatever `domain` error comes back into a
status code. When the order belongs to someone else, that's `domain.ErrForbidden`, which
`writeDomainError` maps to `403`, not `404`. That's a deliberate, debatable choice, not an
oversight: a `403` confirms the order exists, just not for this caller; a `404` would hide even
that much. For a resource where existence itself is sensitive — someone else's private message
thread, say, rather than an order ID nobody could guess anyway — returning `404` for "not yours" and
for "doesn't exist" alike is the more defensible default, at the cost of a slightly worse error
message for legitimate callers who mistyped an ID. This tutorial uses `403` because an order ID is
an opaque UUID with nothing worth hiding behind it; don't copy that choice onto a resource where it
doesn't hold.

`r.PathValue("id")` is Go 1.22+'s stdlib router reading `{id}` out of the route pattern registered
below — no third-party router needed for this. `handleListOrders` adds pagination, clamped rather
than trusted:

```go
const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFromContext(r.Context())

	limit := parseIntParam(r, "limit", defaultListLimit)
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	offset := max(parseIntParam(r, "offset", 0), 0)

	orders, err := s.orders.ListOrders(r.Context(), claims.UserID, limit, offset)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	resp := listOrdersResponse{Orders: make([]orderResponse, len(orders))}
	for i, o := range orders {
		resp.Orders[i] = newOrderResponse(o)
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseIntParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
```

A `?limit=100000` or `?limit=-5` from a client silently clamps to something reasonable instead of
either erroring or, worse, actually trying to return an unbounded result set.

## Wire the routes and build the server

Create `api/server.go`:

```go
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/config"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/service"
)

type Server struct {
	http   *http.Server
	orders *service.OrderService
	auth   *service.AuthService
}

// HTTPAddr and AdminAddr take no prefix: both are spelled that way in
// every deployment already. AdminAddr belongs to this package because it
// is the same concern — serving HTTP — even though main binds that
// listener rather than the graph; see chapter 15.
type Config struct {
	HTTPAddr  string `env:"HTTP_ADDR" envDefault:":8080"`
	AdminAddr string `env:"ADMIN_ADDR" envDefault:":8081"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, "")
}

func New(
	cfg *Config,
	orders *service.OrderService,
	authSvc *service.AuthService,
	issuer *auth.Issuer,
	log *observability.Logger,
) *Server {
	s := &Server{orders: orders, auth: authSvc}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /orders", requireAuth(issuer, s.handleCreateOrder))
	mux.HandleFunc("GET /orders/{id}", requireAuth(issuer, s.handleGetOrder))
	mux.HandleFunc("GET /orders", requireAuth(issuer, s.handleListOrders))

	s.http = &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: recoverMiddleware(log, loggingMiddleware(log, mux)),
	}
	return s
}
```

`"POST /auth/login"` — method and pattern in one string — is Go 1.22+'s stdlib
`http.ServeMux`. It's enough for four routes with no path-parameter conflicts, so there's no
third-party router to introduce or explain; see
[chapter 21](21-alternatives-and-further-reading.md#http-routers) for when one earns its keep.

## Run and Stop — and a bug worth hitting on purpose

Every component so far has gotten `Run`/`Stop`/`Health` more or less for free — a thin wrapper
around something with an obvious "connect" and "disconnect." An `*http.Server` looks like it should
be just as simple:

```go
func (s *Server) Run(ctx context.Context) error {
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api: %w", err)
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
```

This compiles, and it even works — right up until the process needs to actually shut down.
Sending it a real `SIGTERM` hangs forever. Here's why, and it's worth understanding rather than
just copying the fix: servo's generated `App.Run` waits for *every* `Runner` via an `errgroup`, and
only calls `Shutdown` (which calls `Stop`, which calls `http.Server.Shutdown`) once every `Runner`
has already returned:

```go
func (a *App) Run(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return a.server.Run(gctx) })
	g.Go(func() error { return a.notifier.Run(gctx) })
	return g.Wait()
}
```

`ListenAndServe` never observes `ctx` cancellation on its own — it only returns once something
calls `Shutdown` or `Close` on the server. So with the naive `Run` above: `ctx` gets cancelled,
`Run` doesn't notice, `ListenAndServe` keeps blocking, `errgroup.Wait()` never returns, `App.Run`
never returns, and `main()` — which calls `app.Shutdown()` only *after* `app.Run()` returns — never
reaches the line that would have called `Stop`. Everything is waiting for something that's waiting
for it.

The fix is for `Run` to watch `ctx` itself, and simply stop waiting once it's cancelled — `Stop`
still does the actual socket close, moments later, once `Shutdown` is finally reached:

```go
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("api: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}
```

`notifier.Run`, back in [chapter 7](07-messaging-layer.md#build-the-consuming-side), already had
the correct shape (`<-ctx.Done(); return nil`) — the lesson generalizes: **any `Run` your own code
writes must return when its context is cancelled, on its own.** Nothing external can force it to;
`errgroup` only waits, it never interrupts. A regression test pins this down by never calling
`Stop` at all:

```go
func TestRunReturnsPromptlyWhenContextIsCancelled(t *testing.T) {
	// ... construct srv with mocked dependencies ...
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation")
	}
}
```

Against the naive version, this test fails exactly as predicted: `Run did not return within 2s of
context cancellation`. Against the fixed version, it passes in under a millisecond. If `Run` ever
regresses to needing `Stop` to unblock it, this test — not a production incident — is what catches
it.

## Integrating with servo

Nothing in `api/` imports servo, and nothing has to. The server is an ordinary constructor taking
ordinary dependencies, so naming it as a root is the entire integration —
`cmd/orders/spec.go`, covered properly in [chapter 13](13-wiring-with-servo.md):

```go
servo.Build(
	servo.Root[*api.Server](),
	// ... binds, scope, overrides
)
```

`servo generate` works backwards from that: `api.New` needs a `*service.OrderService`, which needs
a repository and a cache, and so on down. `Run` and `Stop` are found structurally — no interface to
implement, no registration — so the generated `App.Run` starts this server and `App.Shutdown` stops
it, in dependency order.

That is the whole of it, and it is the same three lines for the two transports in the next
two chapters.

## What `app.Ready` actually reports

`Health` and `Ready` are two different questions, and an orchestrator does two very different
things with the answers. `Health` means "this process is not broken" — a failure gets the container
restarted. `Ready` means "send me traffic" — a failure only takes the instance out of the load
balancer, and is expected to be temporary. Conflating them is how a slow warm-up turns into a
restart loop.

This server has an honest answer to the second question, and giving it requires one change to
`Run`. `ListenAndServe` hides the moment the port is bound inside itself, so bind explicitly:

```go
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("api: listen: %w", err)
	}
	s.ready.Store(true)
	// ... serve on ln, return when ctx is done
}

// Ready reports whether this server is accepting connections yet.
func (s *Server) Ready(context.Context) error {
	if !s.ready.Load() {
		return errors.New("api: not accepting connections yet")
	}
	return nil
}
```

`ready` is an `atomic.Bool` on the struct: written by `Run`, read by whatever goroutine serves the
probe. The window it describes is real — between the graph finishing construction and `Run` binding
the port, the process is perfectly healthy and cannot serve a single request. Routing traffic there
produces connection refused for no reason at all.

That is the whole of `Readier`. Nothing calls it for you: servo emits `app.Ready(ctx)`, and you
decide when it runs — which is what the next section wires up.

## Health and readiness live outside the graph

`GET /healthz` and `GET /readyz` need `app.Health(ctx)` and `app.Ready(ctx)` — but those are
methods on the fully-*constructed* `App`, and nothing inside the graph (including `api.Server`
itself) can get a reference to `App`, because `App` doesn't exist yet at the point any single
component inside it is being built. This isn't a limitation to work around with a clever
constructor trick; it's just outside what dependency injection is for. The straightforward answer
is to wire these two routes by hand, on a *separate* listener from the API's own — which also
means health checks never compete with real traffic for the same connection pool.

It goes in its own package, `admin/`, rather than in `main.go`. That looks like over-engineering
for two routes until you notice the two companion transports need exactly the same thing: three
copies of a security boundary is three chances to get one wrong. `admin.New` takes an interface
rather than a concrete `*App` precisely so one implementation serves every injector.

```go
// admin/admin.go
package admin

// Checker is the part of a generated servo App this package needs. Taking
// an interface rather than *App is what lets one implementation serve
// every injector, each of which generates its own App type.
type Checker interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}

func New(addr string, app Checker, metrics http.Handler) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", reportHandler(app.Health))
	mux.HandleFunc("GET /readyz", reportHandler(app.Ready))
	mux.Handle("GET /metrics", metrics)
	return &http.Server{Addr: addr, Handler: mux}
}
```

The `metrics` handler is [chapter 15](15-observability.md)'s; it is on this listener for exactly
the same reason the health routes are. Everything served here describes the service's internals —
`/healthz` names every component in the graph along with its status — which is why the deployment
binds this listener to the cluster network and never to the internet.

`reportHandler` renders a `servo.Report` as JSON — but not by encoding it directly.
`servo.NodeStatus` has a `String()` method, but `encoding/json` only ever calls
`MarshalJSON`/`MarshalText`, neither of which it implements, so a bare `json.Marshal(report)`
prints `"Status":0` instead of `"Status":"ok"`. Re-rendering it into a small local type fixes that:

```go
type response struct {
	Clean bool         `json:"clean"`
	Nodes []node `json:"nodes"`
}

type node struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func reportHandler(check func(context.Context) servo.Report) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := check(r.Context())

		resp := response{Clean: report.Clean()}
		for _, n := range report.Nodes {
			node := node{Name: n.Name, Status: n.Status.String()}
			if n.Err != nil {
				node.Error = n.Err.Error()
			}
			resp.Nodes = append(resp.Nodes, node)
		}

		w.Header().Set("Content-Type", "application/json")
		if !report.Clean() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}
```

`AdminAddr` is already on the transport's own `Config`, alongside `HTTPAddr` — the listener is
wired in `main` rather than by the graph, but the address belongs to the same concern.

### These three endpoints stay off the public port

That separation is the point, and it is worth stating plainly: `/healthz` and `/readyz` enumerate
every component in the graph by name along with its status, and `/metrics` ([chapter
15](15-observability.md)) exposes request rates, latencies and error counts per route. Together
they describe the shape and health of the system precisely enough to be worth hiding, so the
deployment binds this listener to the cluster network and no ingress rule points at it.

Boundaries that live only in someone's memory erode, so this is asserted rather than assumed —
here, and in both companion transports:

```go
for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
	resp, err := http.Get(ts.URL + path)
	...
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET %s = %d on the public listener, want 404", path, resp.StatusCode)
	}
}
```

Adding `/metrics` to a public router — the kind of change that looks harmless in review — fails
that test.

## Try the whole thing

Set every required variable and start the service:

```
$ POSTGRES_DSN="postgres://orders:orders@localhost:5432/orders?sslmode=disable" \
  REDIS_ADDR="localhost:6379" \
  NATS_URL="nats://localhost:4222" \
  JWT_SECRET="dev-secret-do-not-use-in-production" \
  go run ./cmd/orders
```

```
$ curl -s -X POST http://localhost:8080/auth/login -d '{"username":"alice","password":"password123"}'
{"token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...YfoSyVgGFq_A4pl5vS9Kr9maUm_p6gl7Ngjp4twzXb0"}
```

```
$ TOKEN=<the token above>
$ curl -s -X POST http://localhost:8080/orders -H "Authorization: Bearer $TOKEN" -d '{"item":"widget","quantity":3}'
{"id":"fc532002-fdd4-4874-a8ae-47a4b8aa0b3d","item":"widget","quantity":3,"status":"pending","created_at":"2026-08-27T12:09:16.182727Z"}
```

```
$ curl -s http://localhost:8080/orders/fc532002-fdd4-4874-a8ae-47a4b8aa0b3d -H "Authorization: Bearer $TOKEN"
{"id":"fc532002-fdd4-4874-a8ae-47a4b8aa0b3d","item":"widget","quantity":3,"status":"pending","created_at":"2026-08-27T12:09:16.182727Z"}

$ curl -s http://localhost:8080/orders -H "Authorization: Bearer $TOKEN"
{"orders":[{"id":"fc532002-fdd4-4874-a8ae-47a4b8aa0b3d","item":"widget","quantity":3,"status":"pending","created_at":"2026-08-27T14:09:16.182727+02:00"}]}
```

And the admin port:

```
$ curl -s http://localhost:8081/healthz
{"clean":true,"nodes":[{"name":"*example.com/servoorders/internal/postgres.Store","status":"ok"},{"name":"*example.com/servoorders/internal/redis.Cache","status":"ok"},{"name":"*example.com/servoorders/internal/natsbroker.Publisher","status":"ok"}]}
```

`/readyz` responds too, but with an empty node list (`{"clean":true,"nodes":null}`) — nothing in
this graph implements `Readier` yet, so there's nothing distinct from `Health` for it to report.
That's not a bug to fix; it's what "no component needs a separately-meaningful readiness signal"
honestly looks like. [Chapter 13](13-wiring-with-servo.md) covers every capability this graph
actually uses, side by side.

## Write it down: openapi/openapi.yaml

Everything above was verified by hand, with `curl`, one endpoint at a time. That's enough to prove
it works; it isn't enough for someone integrating against this API to discover what it promises
without reading the handler source. `openapi/openapi.yaml` writes the same contract
down in a form tooling can consume — client generators, `Try it out`-style documentation viewers,
contract-testing tools. One operation out of the full spec, `GET /orders/{id}`, shown here
(trimmed of its own trailing `content:`/`schema:` blocks and the shared `429` every operation in
the real file also declares — see the file itself for those):

```yaml
paths:
  /orders/{id}:
    get:
      operationId: getOrder
      summary: Get a single order by ID
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
            format: uuid
      responses:
        "200":
          description: The order
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Order"
        "400":
          description: id is not a valid UUID
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Error"
        "401":
          description: Missing, malformed, or expired bearer token
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Error"
        "403":
          description: >
            The order exists but belongs to a different user. Returned
            instead of 404 only because this tutorial's authorization
            model has nothing to hide the order's existence *for* — see
            docs/tutorial/10-api-layer.md's note on when 404 would be the
            more defensible choice instead.
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/Error"
        "404":
          description: No order with that ID exists
```

Every schema in the `components:` section is a direct transcription of the real DTOs in
`api/dto.go` — `Order` mirrors `orderResponse` field for field, right down to `status` being
constrained to the single value `pending` this service ever actually assigns, rather than an
aspirational enum of statuses nothing here implements yet. A spec that describes a richer API than
the code actually serves is worse than no spec at all: it fails silently, by lying, exactly where a
missing spec at least fails honestly by not existing.

The spec deliberately covers only the business API on `HTTPAddr` (port 8080) — `/healthz`,
`/readyz`, and `/metrics` on `AdminAddr` are operational surface for load balancers and Prometheus,
not part of the contract an API consumer integrates against, and none of them take a request body
worth documenting beyond "GET it, read the status code." Validate the spec itself the same way you'd
validate any other generated artifact, rather than trusting it by inspection:

```
$ npx @redocly/cli lint openapi/openapi.yaml
...
openapi/openapi.yaml: validated in 17ms

Woohoo! Your API description is valid. 🎉
You have 2 warnings.
```

The two remaining warnings (no `license` in `info`, and a `localhost` server URL) are both
intentional for a tutorial spec, not oversights left unfixed.

### Serving it, and where

A spec that lives only in the repository drifts from the service. `openapi/openapi.go` embeds it
and serves both the raw document and a browser UI, and all three transports mount the same handler:

```go
//go:embed openapi.yaml
var Spec []byte

// Handler serves /openapi.yaml and /swagger/.
func Handler() http.Handler { ... }
```

Embedding means the binary and its documentation cannot drift apart in a deployment — there is no
file to forget to copy into the image.

It is mounted on the **public** listener, beside the API it describes, which is a deliberate split
from the three endpoints above: the contract tells a caller how to use endpoints they are already
allowed to reach, while health and metrics describe the service's internals. Two more lines at the
top of `api.New`'s mux, above the routes it documents:

```go
mux.Handle("GET /openapi.yaml", openapi.Handler())
mux.Handle("GET /swagger/", openapi.Handler())
```

That is a defensible default and not the only one. Publishing the UI publishes your full endpoint
list, DTO shapes and auth scheme — fine for a public API, not fine for an internal one whose
surface is itself sensitive. `openapi.Handler()` is an ordinary `http.Handler`, so moving it is one
line: mount it in `admin.New` instead, and consumers fetch the spec from inside the cluster or get
it handed to them out of band.

One caveat worth knowing: the UI loads Swagger UI's JavaScript from a CDN rather than vendoring
several megabytes into the repository. A browser with no route to the internet gets an empty page,
and the spec itself is still readable at `/openapi.yaml`. A service that must document itself in an
air-gapped network should vendor the assets and serve them from `openapi/`.

## Diagnostics

- **A client hangs waiting for the process to exit after Ctrl+C** — this chapter's own bug. Check
  every `Run` you write blocks on `<-ctx.Done()` (directly, or via a `select` like `api.Server`'s
  above) rather than only on something that has no idea `ctx` exists.
- **`missing or malformed Authorization header` on a request you're sure has a token** — the header
  value must be exactly `Bearer <token>`, one space, case-sensitive on `Bearer`. A common mistake:
  sending just the raw token with no `Bearer ` prefix at all.
- **A panic in one handler seems to take the whole server down anyway** — confirm
  `recoverMiddleware` is actually the outermost layer
  (`recoverMiddleware(log, loggingMiddleware(log, mux))`, not the other way around) — a panic inside `loggingMiddleware` itself, outside the `recover`,
  would still escape.

## Do's and don'ts

- **Do** validate and clamp query parameters (`limit`, `offset`) rather than trusting them —
  `handleListOrders` never lets a client force an unbounded query.
- **Do** keep DTOs unexported and distinct from domain types, even when they look identical today.
  The day they diverge (a field the API should hide, a field the API should rename) is much easier
  if that boundary already exists.
- **Don't** let a handler construct a `domain.Order` (or any domain type) by hand and pass it
  straight to the repository — every write goes through the service layer, which is where
  validation and orchestration live. A handler's job is translating HTTP into a service call and
  back, nothing more.
- **Don't** log a request body that might contain a password — `handleLogin`'s `loginRequest`
  never gets logged whole anywhere in this codebase; only the method, path, and status do.

## Next

[Chapter 13: Wiring with servo](13-wiring-with-servo.md) — putting every layer built so far into
one spec file and letting `servo generate` do the rest.
