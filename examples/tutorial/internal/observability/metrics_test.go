package observability_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/servoorders/internal/observability"
)

func TestMetricsMiddlewareLabelsByRoutePatternNotRawPath(t *testing.T) {
	m := observability.NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := m.Middleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/orders/11111111-1111-1111-1111-111111111111", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `route="GET /orders/{id}"`) {
		t.Errorf("metrics output missing route=%q label, got:\n%s", "GET /orders/{id}", body)
	}
	if strings.Contains(body, "11111111-1111-1111-1111-111111111111") {
		t.Error("metrics output contains the raw order ID — route should be the pattern, not the path, to avoid unbounded label cardinality")
	}
}

func TestMetricsMiddlewareRecordsTheActualStatusCode(t *testing.T) {
	m := observability.NewMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := m.Middleware(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `status="418"`) {
		t.Errorf("metrics output missing status=\"418\", got:\n%s", rec.Body.String())
	}
}
