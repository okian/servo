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

// Handler exposes the routed, middleware-wrapped handler directly, for
// httptest.NewServer — a real listener bound to cfg.HTTPAddr isn't needed
// (or wanted) to test routing, middleware, and status codes.
func (s *Server) Handler() http.Handler {
	return s.http.Handler
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
