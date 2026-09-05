package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPHandlerWithoutPorts drives the emitted routing, middleware and
// adapters through httptest — no listener, no free-port hunting, the same
// seam the tutorial's hand-written server exposes as Handler().
func TestHTTPHandlerWithoutPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		if r := app.Shutdown(sctx); !r.Clean() {
			t.Errorf("Shutdown not clean: %v", r)
		}
	})

	t.Run("default group serves without a listener", func(t *testing.T) {
		h := app.HTTPHandler("")
		if h == nil {
			t.Fatalf("no default-group handler")
		}
		req := httptest.NewRequest(http.MethodPost, "/order/espresso/?priority=2", strings.NewReader(`{"item":"latte","quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
		}
		// The middleware chain wraps this handler too.
		if w.Header().Get("X-Request-Id") == "" {
			t.Fatalf("server-level middleware missing from the handler seam")
		}
	})

	t.Run("groups are separate handlers", func(t *testing.T) {
		tel := app.HTTPHandler("telemetry")
		if tel == nil {
			t.Fatalf("no telemetry handler")
		}
		w := httptest.NewRecorder()
		tel.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("healthz = %d", w.Code)
		}
		if app.HTTPHandler("nope") != nil {
			t.Fatalf("unknown group must return nil")
		}
	})
}
