package resilience_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/resilience"
)

func TestRateLimiterAllowsRequestsWithinTheLimit(t *testing.T) {
	rl := resilience.NewRateLimiter(resilience.Config{RPS: 1000}, observability.NewMetrics())
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// This is the case api_test.go's fixtures have to deliberately avoid: a
// low (here, zero) RateLimitRPS means the burst is the only capacity the
// limiter ever has, and it's never refilled.
func TestRateLimiterRejectsRequestsOverTheLimitAndCountsIt(t *testing.T) {
	metrics := observability.NewMetrics()
	rl := resilience.NewRateLimiter(resilience.Config{RPS: 0}, metrics) // burst clamped to 1
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("second request status = %d, want 429", second.Code)
	}

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "orders_rate_limit_rejections_total 1") {
		t.Errorf("metrics output missing orders_rate_limit_rejections_total 1, got:\n%s", rec.Body.String())
	}
}
