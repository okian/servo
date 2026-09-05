package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverTurnsPanicInto500(t *testing.T) {
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom: secret detail")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, "Internal Server Error") || strings.Contains(got, "secret detail") {
		t.Fatalf("body = %q — canonical text only, never the panic value", got)
	}
}

func TestRecoverPassesThrough(t *testing.T) {
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("code = %d", w.Code)
	}
}

// net/http's own abort sentinel must keep panicking — it is how a handler
// deliberately tears a connection down, and swallowing it breaks that.
func TestRecoverReraisesAbortHandler(t *testing.T) {
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if recover() == nil {
			t.Fatalf("ErrAbortHandler must propagate")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}
