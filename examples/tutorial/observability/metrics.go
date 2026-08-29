package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry        *prometheus.Registry
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "orders_http_requests_total",
			Help: "Total HTTP requests, by method, route, and status.",
		}, []string{"method", "route", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "orders_http_request_duration_seconds",
			Help: "HTTP request duration in seconds, by method and route.",
		}, []string{"method", "route"}),
	}
	m.registry.MustRegister(m.requestsTotal, m.requestDuration)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// NewCounter registers a counter into this same registry, for any other
// component (resilience.RateLimiter, for one) that wants its own metric on
// the same /metrics endpoint without either package needing a shared
// global — see docs/tutorial/16-resilience.md.
func (m *Metrics) NewCounter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	m.registry.MustRegister(c)
	return c
}

// Middleware labels every metric by r.Pattern (the matched route template,
// e.g. "GET /orders/{id}") rather than r.URL.Path (e.g. "/orders/<uuid>").
// A label with one distinct value per order ID is exactly the unbounded
// cardinality that turns a small Prometheus instance into a struggling one
// — see docs/tutorial/15-observability.md.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.requestsTotal.WithLabelValues(r.Method, route, strconv.Itoa(sw.status)).Inc()
		m.requestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
