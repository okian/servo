package middleware

import (
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimitConfig is provided by your own provider. KeyFunc decides what a
// "client" is — nil means the client IP (the port stripped from
// r.RemoteAddr). Being a real Go struct built in your code, the func field
// is ordinary Go: key by API token, tenant, whatever the request carries.
type RateLimitConfig struct {
	// RPS is the sustained refill rate per key. Must be positive.
	RPS float64
	// Burst is the bucket size — how many requests a cold key may spend at
	// once. Must be at least 1.
	Burst int
	// KeyFunc extracts the limit key; nil means client IP.
	KeyFunc func(*http.Request) string
}

// RateLimit is a hand-rolled token bucket per key: the runtime stays
// stdlib-only, and the state is one small struct per active client, swept
// when idle. Exhausted keys get 429 with a Retry-After. The buckets are per
// process — two replicas mean two buckets per key.
type RateLimit struct {
	rps   float64
	burst float64
	key   func(*http.Request) string

	mu       sync.Mutex
	buckets  map[string]*bucket
	requests int
	// now is time.Now, replaceable in tests — a limiter that can only be
	// tested by sleeping is a limiter that never gets tested.
	now func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimit(cfg *RateLimitConfig) (*RateLimit, error) {
	if cfg.RPS <= 0 {
		return nil, errors.New("middleware: RateLimitConfig.RPS must be positive")
	}
	if cfg.Burst < 1 {
		return nil, errors.New("middleware: RateLimitConfig.Burst must be at least 1")
	}
	key := cfg.KeyFunc
	if key == nil {
		key = clientIP
	}
	return &RateLimit{
		rps:     cfg.RPS,
		burst:   float64(cfg.Burst),
		key:     key,
		buckets: map[string]*bucket{},
		now:     time.Now,
	}, nil
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (m *RateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter, ok := m.take(m.key(r)); !ok {
			w.Header().Set("Retry-After", retryAfter)
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take spends one token from key's bucket, reporting the Retry-After value
// when none is available.
func (m *RateLimit) take(key string) (retryAfter string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.requests++
	if m.requests%4096 == 0 {
		m.sweep(now)
	}

	b := m.buckets[key]
	if b == nil {
		b = &bucket{tokens: m.burst, last: now}
		m.buckets[key] = b
	} else {
		b.tokens = min(m.burst, b.tokens+now.Sub(b.last).Seconds()*m.rps)
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return "", true
	}
	return strconv.Itoa(int(math.Ceil((1 - b.tokens) / m.rps))), false
}

// sweep drops buckets idle long enough to have fully refilled anyway —
// keeping them would only let a slow scan of the key space grow the map.
func (m *RateLimit) sweep(now time.Time) {
	for key, b := range m.buckets {
		if now.Sub(b.last) > 5*time.Minute {
			delete(m.buckets, key)
		}
	}
}
