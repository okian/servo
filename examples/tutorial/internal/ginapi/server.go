// Package ginapi is the Gin transport: the same routes, request shapes and
// error mapping as package api, implemented with github.com/gin-gonic/gin
// instead of net/http.
//
// It exists so a reader can compare the two directly. Everything below the
// transport — service, repository, cache, broker, session — is shared and
// unchanged, which is the point: swapping the HTTP framework touches one
// package and one spec file, and nothing else in the graph notices.
package ginapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/config"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/openapi"
	"example.com/servoorders/internal/resilience"
	"example.com/servoorders/internal/service"
	"example.com/servoorders/internal/session"
)

type Server struct {
	http    *http.Server
	ready   atomic.Bool
	orders  *service.OrderService
	auth    *service.AuthService
	metrics *observability.Metrics
	// sessions is the scope accessor, not a session. Holding a *Session
	// here would pin one user's session for the life of the process, and
	// `servo generate` refuses to emit that — see
	// docs/tutorial/14-scoped-instances.md.
	sessions session.Sessions
}

// Config is this package's own, not shared with api.Config: two transport
// variants that both wanted HTTP_ADDR would otherwise have to agree on one
// type, and a reader swapping between them would have to know that.
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

	// ReleaseMode suppresses Gin's startup banner and its debug-level
	// route dump, both of which write to stdout outside the structured
	// logger. Set explicitly rather than left to GIN_MODE, so the output
	// does not depend on an environment variable nothing else reads.
	gin.SetMode(gin.ReleaseMode)

	// gin.New(), not gin.Default(): Default installs Gin's own Logger and
	// Recovery, which write their own text format. This service already
	// has structured logging and its own recovery, both taking the
	// injected logger.
	r := gin.New()
	r.Use(recoverMiddleware(log), loggingMiddleware(log))

	// The contract and its UI, on the public listener beside the API they
	// describe. Health, readiness and metrics are deliberately *not* here
	// — they live on the admin listener; see cmd/ordersgin/main.go.
	r.Any("/openapi.yaml", gin.WrapH(openapi.Handler()))
	r.Any("/swagger/*any", gin.WrapH(openapi.Handler()))

	r.POST("/auth/login", s.handleLogin)

	// The group is the difference worth seeing: net/http wraps each
	// handler in requireAuth individually, while every route registered
	// on this group inherits it. Forgetting the wrapper on one route is a
	// mistake the group shape cannot make.
	authed := r.Group("/", requireAuth(issuer))
	authed.POST("/orders", s.handleCreateOrder)
	authed.GET("/orders/:id", s.handleGetOrder)
	authed.GET("/orders", s.handleListOrders)
	authed.GET("/me/recent", s.handleRecent)

	// Gin owns routing and the two innermost middlewares; the outer three
	// stay net/http handlers, because they are transport-agnostic and
	// shared verbatim with the api variant. A gin.HandlerFunc wrapper
	// around each would buy nothing and would mean maintaining two copies.
	//
	// Order matches api/server.go: metrics sits directly against the
	// router, tracer outside it, and the limiter outermost of the three so
	// a rejected request costs no span — see docs/tutorial/16-resilience.md.
	var handler http.Handler = r
	handler = metrics.Middleware(handler)
	handler = tracer.Middleware(handler)
	handler = limiter.Middleware(handler)

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
// binary never collide registering the same metric name twice. main.go
// reaches for this directly since /metrics lives on the admin server.
func (s *Server) MetricsHandler() http.Handler {
	return s.metrics.Handler()
}

// Run must return once ctx is cancelled, on its own — servo's generated
// App.Run waits for every Runner via errgroup, and only calls Shutdown
// after every Runner has returned. ListenAndServe never observes ctx
// cancellation by itself. See api/server.go for the longer version.
func (s *Server) Run(ctx context.Context) error {
	// Bound explicitly so Ready has a precise moment to report; see the
	// api variant for the longer version.
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("ginapi: listen: %w", err)
	}
	s.ready.Store(true)

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("ginapi: %w", err)
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

// Ready reports whether this server is accepting connections yet —
// "send me traffic", as distinct from Health's "this process is not
// broken". See the api variant for why the two are kept apart.
func (s *Server) Ready(context.Context) error {
	if !s.ready.Load() {
		return errors.New("ginapi: not accepting connections yet")
	}
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
