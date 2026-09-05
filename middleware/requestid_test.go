package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDEchoesExisting(t *testing.T) {
	var inCtx string
	h := NewRequestID().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inCtx = RequestIDFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", "req-42")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Header().Get("X-Request-Id") != "req-42" || inCtx != "req-42" {
		t.Fatalf("header=%q ctx=%q", w.Header().Get("X-Request-Id"), inCtx)
	}
}

func TestRequestIDGenerates(t *testing.T) {
	var first, second string
	h := NewRequestID().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first == "" {
			first = RequestIDFromContext(r.Context())
		} else {
			second = RequestIDFromContext(r.Context())
		}
	}))
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, "/x", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if first == "" || first == second {
		t.Fatalf("generated ids must be non-empty and distinct: %q vs %q", first, second)
	}
	if w1.Header().Get("X-Request-Id") != first {
		t.Fatalf("response header %q != ctx id %q", w1.Header().Get("X-Request-Id"), first)
	}
}

func TestRequestIDFromContextZero(t *testing.T) {
	if got := RequestIDFromContext(httptest.NewRequest(http.MethodGet, "/x", nil).Context()); got != "" {
		t.Fatalf("no middleware, no id — got %q", got)
	}
}
