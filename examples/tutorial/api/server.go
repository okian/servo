// Package api is the HTTP transport: routing, request/response shapes, and
// the middleware chain. It depends on service.OrderService and
// service.AuthService — never on repository, cache, or broker directly.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"example.com/servoorders/auth"
	"example.com/servoorders/config"
	"example.com/servoorders/observability"
	"example.com/servoorders/openapi"
	"example.com/servoorders/resilience"
	"example.com/servoorders/service"
	"example.com/servoorders/session"
)

type Server struct {
	http    *http.Server
	orders  *service.OrderService
	auth    *service.AuthService
	metrics *observability.Metrics
	// sessions is the scope accessor, not a session. Holding a *Session
	// here would pin one user's session for the life of the process, and
	// `servo generate` refuses to emit that — see
	// docs/tutorial/12-scoped-instances.md.
	sessions session.Sessions
}

// Config carries the two listen addresses. AdminAddr is here rather than
// in its own package because it belongs to the same concern — serving
// HTTP — even though the admin listener itself is wired in main rather
// than through the graph; see newAdminServer.
//
// No prefix: HTTP_ADDR and ADMIN_ADDR are spelled that way in every
// deployment already.
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
	metrics *observability.Metrics,
	tracer *observability.Tracer,
	limiter *resilience.RateLimiter,
	sessions session.Sessions,
	log *observability.Logger,
) *Server {
	s := &Server{orders: orders, auth: authSvc, metrics: metrics, sessions: sessions}

	mux := http.NewServeMux()

	// The contract and its UI, on the public listener beside the API they
	// describe. Health, readiness and metrics are deliberately not here —
	// they live on the admin listener; see package admin.
	mux.Handle("GET /openapi.yaml", openapi.Handler())
	mux.Handle("GET /swagger/", openapi.Handler())

	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /orders", requireAuth(issuer, s.handleCreateOrder))
	mux.HandleFunc("GET /orders/{id}", requireAuth(issuer, s.handleGetOrder))
	mux.HandleFunc("GET /orders", requireAuth(issuer, s.handleListOrders))
	mux.HandleFunc("GET /me/recent", requireAuth(issuer, s.handleRecent))

	// Outermost first: recover must see a panic from anything below it.
	// Metrics has to sit directly against logging/mux, not further out —
	// otelhttp's handler (inside tracer.Middleware) forks the request via
	// r.WithContext before passing it on, and http.ServeMux sets r.Pattern
	// on that fork, not on whatever *http.Request an outer middleware is
	// still holding. Metrics outside tracer would read an empty Pattern on
	// every request, not just rejected ones — see
	// docs/tutorial/14-resilience.md. limiter sits outside tracer so a
	// rejected request costs no span; it counts its own rejections
	// directly (see resilience.RateLimiter) rather than trying to route
	// them through requests_total, which a rejected request never reaches
	// anyway.
	handler := loggingMiddleware(log, mux)
	handler = metrics.Middleware(handler)
	handler = tracer.Middleware(handler)
	handler = limiter.Middleware(handler)
	handler = recoverMiddleware(log, handler)

	s.http = &http.Server{Addr: cfg.HTTPAddr, Handler: handler}
	return s
}

// Handler exposes the routed, middleware-wrapped handler directly, for
// httptest.NewServer — a real listener bound to cfg.HTTPAddr isn't needed
// (or wanted) to test routing, middleware, and status codes.
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// MetricsHandler exposes this server's own metrics registry — not the
// global default one, so multiple *Server instances in the same test
// binary (or a normal one and a NewTestApp one) never collide trying to
// register the same metric name twice. main.go reaches for this directly
// (app.server.MetricsHandler()) since /metrics lives on the admin server,
// not this one — see docs/tutorial/13-observability.md.
func (s *Server) MetricsHandler() http.Handler {
	return s.metrics.Handler()
}

// Run must return once ctx is cancelled, on its own — servo's generated
// App.Run waits for every Runner via errgroup, and only calls Shutdown
// (which calls Stop, which calls http.Server.Shutdown) after every Runner
// has already returned. ListenAndServe never observes ctx cancellation by
// itself, so without the select below, Run would block forever and
// Shutdown would never be reached at all. Stop still does the actual
// socket close; Run's job here is only to stop waiting once told to.
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

func (s *Server) Stop(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
