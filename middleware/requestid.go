package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDKey struct{}

// RequestID reads X-Request-Id or generates one, sets it on the response,
// and plants it in the context for handlers (and extractors) to read back
// with RequestIDFromContext. Zero config.
type RequestID struct{}

func NewRequestID() *RequestID { return &RequestID{} }

func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			var buf [16]byte
			_, _ = rand.Read(buf[:]) // crypto/rand.Read never fails on supported platforms
			id = hex.EncodeToString(buf[:])
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFromContext returns the id RequestID planted, or "" outside it.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
