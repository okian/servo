package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimeoutCutsOffSlowHandlers(t *testing.T) {
	to, err := NewTimeout(&TimeoutConfig{Limit: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewTimeout: %v", err)
	}
	h := to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "timed out") {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestTimeoutPassesFastHandlers(t *testing.T) {
	to, err := NewTimeout(&TimeoutConfig{Limit: time.Second})
	if err != nil {
		t.Fatalf("NewTimeout: %v", err)
	}
	h := to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestTimeoutValidatesConfig(t *testing.T) {
	if _, err := NewTimeout(&TimeoutConfig{}); err == nil {
		t.Fatalf("Limit must be positive")
	}
}
