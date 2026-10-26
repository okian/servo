package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func corsHandler(t *testing.T, cfg *CORSConfig) http.Handler {
	t.Helper()
	c, err := NewCORS(cfg)
	if err != nil {
		t.Fatalf("NewCORS: %v", err)
	}
	return c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORSPreflight(t *testing.T) {
	h := corsHandler(t, &CORSConfig{
		AllowedOrigins: []string{"https://app.example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"X-Token"},
		MaxAge:         10 * time.Minute,
	})

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204 (preflights never reach the handler)", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("Allow-Methods = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "X-Token" {
		t.Errorf("Allow-Headers = %q", got)
	}
	if got := w.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Errorf("Max-Age = %q", got)
	}
}

func TestCORSActualRequest(t *testing.T) {
	h := corsHandler(t, &CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Allow-Credentials = %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); got != "X-Request-Id" {
		t.Errorf("Expose-Headers = %q", got)
	}
	if got := w.Header().Values("Vary"); len(got) == 0 {
		t.Errorf("Vary must include Origin")
	}
}

// With credentials allowed, a wildcard config still echoes the concrete
// origin — Access-Control-Allow-Origin: * is invalid alongside credentials.
func TestCORSWildcardWithCredentialsEchoesOrigin(t *testing.T) {
	h := corsHandler(t, &CORSConfig{AllowedOrigins: []string{"*"}, AllowCredentials: true})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://any.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://any.example.com" {
		t.Fatalf("Allow-Origin = %q, want the echoed origin", got)
	}
}

// A disallowed origin gets no CORS headers at all — the browser enforces
// the block; the request itself still runs.
func TestCORSDisallowedOrigin(t *testing.T) {
	h := corsHandler(t, &CORSConfig{AllowedOrigins: []string{"https://app.example.com"}})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("code=%d allow-origin=%q", w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSRequiresOrigins(t *testing.T) {
	if _, err := NewCORS(&CORSConfig{}); err == nil {
		t.Fatalf("empty AllowedOrigins must be rejected")
	}
}

// Without credentials a wildcard config answers with "*" itself, which
// lets a cache share one response between every origin.
func TestCORSWildcardWithoutCredentialsAllowsAny(t *testing.T) {
	h := corsHandler(t, &CORSConfig{AllowedOrigins: []string{"*"}})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://any.example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Allow-Credentials = %q, want none", got)
	}
}

// A request without an Origin is not a cross-origin one: it runs with no
// CORS headers, and with no Vary either, since nothing about its response
// depends on an origin.
func TestCORSSameOriginRequestIsUntouched(t *testing.T) {
	h := corsHandler(t, &CORSConfig{AllowedOrigins: []string{"*"}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want the handler's 200", w.Code)
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "Vary"} {
		if got := w.Header().Values(name); len(got) != 0 {
			t.Errorf("%s = %q, want none", name, got)
		}
	}
}

// With no AllowedHeaders configured, a preflight is told yes to the
// headers it asked for, and with credentials allowed it says so; the
// methods are every one servo routes, and with no MaxAge there is no
// Max-Age header.
func TestCORSPreflightEchoesTheRequestedHeaders(t *testing.T) {
	h := corsHandler(t, &CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowCredentials: true})
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "X-Token, Content-Type")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", w.Code)
	}
	for name, want := range map[string]string{
		"Access-Control-Allow-Origin":      "https://app.example.com",
		"Access-Control-Allow-Headers":     "X-Token, Content-Type",
		"Access-Control-Allow-Credentials": "true",
		"Access-Control-Allow-Methods":     "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS",
		"Access-Control-Max-Age":           "",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
