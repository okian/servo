package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func rateLimited(t *testing.T, cfg *RateLimitConfig) (*RateLimit, http.Handler) {
	t.Helper()
	rl, err := NewRateLimit(cfg)
	if err != nil {
		t.Fatalf("NewRateLimit: %v", err)
	}
	return rl, rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func hit(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRateLimitBurstThenRefill(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 2})
	current := time.Now()
	rl.now = func() time.Time { return current }

	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("first request = %d", w.Code)
	}
	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("second request = %d (burst is 2)", w.Code)
	}
	w := hit(h, "10.0.0.1:1234")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatalf("429 must carry Retry-After")
	}

	// One second at 1 RPS refills one token.
	current = current.Add(time.Second)
	if w := hit(h, "10.0.0.1:1234"); w.Code != http.StatusOK {
		t.Fatalf("post-refill request = %d", w.Code)
	}
}

// Buckets are per key — by default the client IP, ports ignored.
func TestRateLimitKeysAreIndependent(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1, Burst: 1})
	current := time.Now()
	rl.now = func() time.Time { return current }

	if w := hit(h, "10.0.0.1:1111"); w.Code != http.StatusOK {
		t.Fatalf("first client = %d", w.Code)
	}
	if w := hit(h, "10.0.0.1:2222"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("same IP, different port = %d, want 429 (one bucket per IP)", w.Code)
	}
	if w := hit(h, "10.0.0.2:1111"); w.Code != http.StatusOK {
		t.Fatalf("different IP = %d, want its own bucket", w.Code)
	}
}

func TestRateLimitCustomKey(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{
		RPS: 1, Burst: 1,
		KeyFunc: func(r *http.Request) string { return r.Header.Get("X-Api-Key") },
	})
	current := time.Now()
	rl.now = func() time.Time { return current }

	req := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.Header.Set("X-Api-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := req("a"); w.Code != http.StatusOK {
		t.Fatalf("key a = %d", w.Code)
	}
	if w := req("a"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("key a again = %d", w.Code)
	}
	if w := req("b"); w.Code != http.StatusOK {
		t.Fatalf("key b = %d", w.Code)
	}
}

func TestRateLimitValidatesConfig(t *testing.T) {
	if _, err := NewRateLimit(&RateLimitConfig{RPS: 0, Burst: 1}); err == nil {
		t.Fatalf("RPS must be positive")
	}
	if _, err := NewRateLimit(&RateLimitConfig{RPS: 1, Burst: 0}); err == nil {
		t.Fatalf("Burst must be at least 1")
	}
}
