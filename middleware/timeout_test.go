package middleware

import (
	"context"
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
	// net/http sniffs the default body as text/plain.
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q: the JSON error must say it is JSON", got)
	}
}

// A body of your own is sent as JSON when it is JSON; anything else is left
// to the type net/http sniffs, as before.
func TestTimeoutTypesConfiguredBody(t *testing.T) {
	for _, tc := range []struct{ body, wantType string }{
		{`{"code":"TIMEOUT"}`, "application/json"},
		{"<h1>slow</h1>", "text/html; charset=utf-8"},
	} {
		to, err := NewTimeout(&TimeoutConfig{Limit: 10 * time.Millisecond, Body: tc.body})
		if err != nil {
			t.Fatalf("NewTimeout: %v", err)
		}
		srv := httptest.NewServer(to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		})))
		res, err := http.Get(srv.URL)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		_ = res.Body.Close()
		srv.Close()
		if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Content-Type") != tc.wantType {
			t.Fatalf("%q: status %d, Content-Type %q, want %q", tc.body, res.StatusCode, res.Header.Get("Content-Type"), tc.wantType)
		}
	}
}

// A 503 the handler returned itself is not the timeout's: it keeps its own
// type, or its lack of one.
func TestTimeoutLeavesHandlerServiceUnavailableAlone(t *testing.T) {
	to, err := NewTimeout(&TimeoutConfig{Limit: time.Second})
	if err != nil {
		t.Fatalf("NewTimeout: %v", err)
	}
	for _, contentType := range []string{"text/plain; charset=utf-8", ""} {
		h := to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("busy"))
		}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusServiceUnavailable || w.Body.String() != "busy" || w.Header().Get("Content-Type") != contentType {
			t.Fatalf("code=%d body=%q Content-Type=%q, want the handler's own %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"), contentType)
		}
	}
}

// When the request's context ends for another reason than the limit — the
// client went away — http.TimeoutHandler writes its 503 with no body; the
// status held back for a body still goes out.
func TestTimeoutReleasesBodylessServiceUnavailable(t *testing.T) {
	to, err := NewTimeout(&TimeoutConfig{Limit: time.Second})
	if err != nil {
		t.Fatalf("NewTimeout: %v", err)
	}
	// The handler outlasts ServeHTTP, so the client leaving is the only
	// way out of it.
	release := make(chan struct{})
	h := to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx))
	close(release)
	if w.Code != http.StatusServiceUnavailable || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatalf("code=%d body=%q Content-Type=%q, want a bare 503", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
}

// The documented limits are http.TimeoutHandler's own; if net/http ever
// lifts this one, the godoc and docs/reference/middleware.md need updating.
func TestTimeoutHandlerCannotFlush(t *testing.T) {
	to, err := NewTimeout(&TimeoutConfig{Limit: time.Second})
	if err != nil {
		t.Fatalf("NewTimeout: %v", err)
	}
	var isFlusher bool
	var flushErr error
	to.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, isFlusher = w.(http.Flusher)
		flushErr = http.NewResponseController(w).Flush()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if isFlusher || flushErr == nil {
		t.Fatalf("Flusher=%v, ResponseController.Flush=%v: the documented limit no longer holds", isFlusher, flushErr)
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
