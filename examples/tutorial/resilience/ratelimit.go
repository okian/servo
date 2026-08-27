package resilience

import (
	"net/http"

	"example.com/servoorders/config"
	"example.com/servoorders/observability"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
)

// RateLimiter is one shared bucket for the whole service, not one per
// client — the simplest thing that actually protects the process from
// being overwhelmed. A real multi-tenant service usually wants one bucket
// per client (keyed by API key or IP), which trades this simplicity for
// needing an eviction strategy so the map of limiters doesn't grow
// forever; see docs/tutorial/13-resilience.md.
type RateLimiter struct {
	limiter    *rate.Limiter
	rejections prometheus.Counter
}

func NewRateLimiter(cfg *config.Config, metrics *observability.Metrics) *RateLimiter {
	burst := max(int(cfg.RateLimitRPS), 1)
	return &RateLimiter{
		limiter:    rate.NewLimiter(rate.Limit(cfg.RateLimitRPS), burst),
		rejections: metrics.NewCounter("orders_rate_limit_rejections_total", "Total requests rejected by the rate limiter."),
	}
}

// Middleware deliberately doesn't feed rejections through Metrics'
// requests_total/duration — those are labeled by r.Pattern, and a request
// this middleware rejects never reaches the mux that sets it. Counting it
// under a synthetic "unmatched" route would say less than a purpose-built
// counter does. See docs/tutorial/13-resilience.md.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.limiter.Allow() {
			rl.rejections.Inc()
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
