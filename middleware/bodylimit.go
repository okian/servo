package middleware

import (
	"errors"
	"net/http"
)

// BodyLimitConfig is provided by your own provider.
type BodyLimitConfig struct {
	// MaxBytes caps every request body in scope. Must be positive.
	MaxBytes int64
}

// BodyLimit is the blanket request-body cap: a declared Content-Length over
// the limit is refused with 413 before the handler runs, and everything
// else (chunked included) reads through http.MaxBytesReader so the cap
// holds on the read path too. It complements HTTPConfig.MaxBodyBytes, which
// guards only the routes that decode bodies.
type BodyLimit struct {
	max int64
}

func NewBodyLimit(cfg *BodyLimitConfig) (*BodyLimit, error) {
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("middleware: BodyLimitConfig.MaxBytes must be positive")
	}
	return &BodyLimit{max: cfg.MaxBytes}, nil
}

func (m *BodyLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > m.max {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, m.max)
		}
		next.ServeHTTP(w, r)
	})
}
