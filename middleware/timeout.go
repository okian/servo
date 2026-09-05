package middleware

import (
	"errors"
	"net/http"
	"time"
)

// TimeoutConfig is provided by your own provider.
type TimeoutConfig struct {
	// Limit is how long a handler may run. Must be positive.
	Limit time.Duration
	// Body is the 503 payload; empty means the JSON error default.
	Body string
}

// Timeout wraps http.TimeoutHandler: a handler exceeding the limit gets cut
// off with a 503 and the configured body, and its context is cancelled so
// well-behaved work stops.
type Timeout struct {
	limit time.Duration
	body  string
}

func NewTimeout(cfg *TimeoutConfig) (*Timeout, error) {
	if cfg.Limit <= 0 {
		return nil, errors.New("middleware: TimeoutConfig.Limit must be positive")
	}
	body := cfg.Body
	if body == "" {
		body = `{"error":"request timed out"}`
	}
	return &Timeout{limit: cfg.Limit, body: body}, nil
}

func (m *Timeout) Middleware(next http.Handler) http.Handler {
	return http.TimeoutHandler(next, m.limit, m.body)
}
