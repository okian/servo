package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// An inbound id that is too long or not printable ASCII is replaced, not
// echoed: it ends up in logs and response headers.
func TestRequestIDReplacesUnusableInbound(t *testing.T) {
	for _, bad := range []string{strings.Repeat("x", maxRequestIDLen+1), "has space", "ctl\x01", "ünïcode"} {
		var inCtx string
		h := NewRequestID().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inCtx = RequestIDFromContext(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-Request-Id", bad)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if inCtx == "" || inCtx == bad || w.Header().Get("X-Request-Id") != inCtx {
			t.Fatalf("%q: ctx=%q header=%q", bad, inCtx, w.Header().Get("X-Request-Id"))
		}
	}
}

// The longest acceptable id is still echoed.
func TestRequestIDKeepsMaximumLengthInbound(t *testing.T) {
	id := strings.Repeat("a", maxRequestIDLen)
	h := NewRequestID().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("X-Request-Id") != id {
		t.Fatalf("header = %q, want the inbound id echoed", w.Header().Get("X-Request-Id"))
	}
}
