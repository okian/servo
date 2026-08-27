# 10. API layer

Everything up to this chapter has been assembled but never actually reachable from outside the
process. This chapter wires it to real HTTP: routes, request and response shapes, and the
middleware that runs on every request. By the end, `curl` will be able to log in, create an order,
and read it back — for real, against everything built in chapters 5 through 9.

## Define the request and response shapes first

The API's JSON shapes are their own thing, separate from `domain.Order` — a domain type can gain a
field for internal reasons without that automatically becoming part of the public API contract.
Create `api/dto.go`:

```go
package api

import (
	"time"

	"example.com/servoorders/domain"
	"github.com/google/uuid"
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

## Build the middleware chain

Two cross-cutting concerns apply to (almost) every request: authentication, and not letting a
panic in one handler take down every other in-flight request. Create `api/middleware.go`. Start
with how a verified request identifies its caller — a `context.Context` key, and the two functions
that use it:

```go
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"example.com/servoorders/auth"
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
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(r.Context(), "api: panic recovered", "panic", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.InfoContext(r.Context(), "request",
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
intentionally bare-bones; [chapter 12](12-observability.md) replaces it with something that
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

	"example.com/servoorders/domain"
	"github.com/google/uuid"
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

	"example.com/servoorders/auth"
	"example.com/servoorders/config"
	"example.com/servoorders/service"
)

type Server struct {
	http   *http.Server
	orders *service.OrderService
	auth   *service.AuthService
}

func New(cfg *config.Config, orders *service.OrderService, authSvc *service.AuthService, issuer *auth.Issuer) *Server {
	s := &Server{orders: orders, auth: authSvc}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /orders", requireAuth(issuer, s.handleCreateOrder))
	mux.HandleFunc("GET /orders/{id}", requireAuth(issuer, s.handleGetOrder))
	mux.HandleFunc("GET /orders", requireAuth(issuer, s.handleListOrders))

	s.http = &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: recoverMiddleware(loggingMiddleware(mux)),
	}
	return s
}
```

`"POST /auth/login"` — method and pattern in one string — is Go 1.22+'s stdlib
`http.ServeMux`. It's enough for four routes with no path-parameter conflicts, so there's no
third-party router to introduce or explain; see
[chapter 18](18-alternatives-and-further-reading.md#http-routers) for when one earns its keep.

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

## Health and readiness live outside the graph

`GET /healthz` and `GET /readyz` need `app.Health(ctx)` and `app.Ready(ctx)` — but those are
methods on the fully-*constructed* `App`, and nothing inside the graph (including `api.Server`
itself) can get a reference to `App`, because `App` doesn't exist yet at the point any single
component inside it is being built. This isn't a limitation to work around with a clever
constructor trick; it's just outside what dependency injection is for. The straightforward answer
is to wire these two routes by hand, in `main.go`, on a *separate* listener from `api.Server`'s own
— which also means health checks never compete with real traffic for the same connection pool:

```go
// cmd/orders/main.go
func newAdminServer(addr string, app interface {
	Health(context.Context) servo.Report
	Ready(context.Context) servo.Report
}) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", reportHandler(app.Health))
	mux.HandleFunc("GET /readyz", reportHandler(app.Ready))
	return &http.Server{Addr: addr, Handler: mux}
}
```

`reportHandler` renders a `servo.Report` as JSON — but not by encoding it directly.
`servo.NodeStatus` has a `String()` method, but `encoding/json` only ever calls
`MarshalJSON`/`MarshalText`, neither of which it implements, so a bare `json.Marshal(report)`
prints `"Status":0` instead of `"Status":"ok"`. Re-rendering it into a small local type fixes that:

```go
type healthResponse struct {
	Clean bool         `json:"clean"`
	Nodes []healthNode `json:"nodes"`
}

type healthNode struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func reportHandler(check func(context.Context) servo.Report) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := check(r.Context())

		resp := healthResponse{Clean: report.Clean()}
		for _, n := range report.Nodes {
			node := healthNode{Name: n.Name, Status: n.Status.String()}
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

Add one more field to `Config` for this, back in `config/config.go`:

```go
AdminAddr string `env:"ADMIN_ADDR" envDefault:":8081"`
```

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
{"clean":true,"nodes":[{"name":"*example.com/servoorders/postgres.Store","status":"ok"},{"name":"*example.com/servoorders/redis.Cache","status":"ok"},{"name":"*example.com/servoorders/natsbroker.Publisher","status":"ok"}]}
```

`/readyz` responds too, but with an empty node list (`{"clean":true,"nodes":null}`) — nothing in
this graph implements `Readier` yet, so there's nothing distinct from `Health` for it to report.
That's not a bug to fix; it's what "no component needs a separately-meaningful readiness signal"
honestly looks like. [Chapter 11](11-wiring-with-servo.md) covers every capability this graph
actually uses, side by side.

## Diagnostics

- **A client hangs waiting for the process to exit after Ctrl+C** — this chapter's own bug. Check
  every `Run` you write blocks on `<-ctx.Done()` (directly, or via a `select` like `api.Server`'s
  above) rather than only on something that has no idea `ctx` exists.
- **`missing or malformed Authorization header` on a request you're sure has a token** — the header
  value must be exactly `Bearer <token>`, one space, case-sensitive on `Bearer`. A common mistake:
  sending just the raw token with no `Bearer ` prefix at all.
- **A panic in one handler seems to take the whole server down anyway** — confirm
  `recoverMiddleware` is actually the outermost layer (`recoverMiddleware(loggingMiddleware(mux))`,
  not the other way around) — a panic inside `loggingMiddleware` itself, outside the `recover`,
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

[Chapter 11: Wiring with servo](11-wiring-with-servo.md) — putting every layer built so far into
one spec file and letting `servo generate` do the rest.
