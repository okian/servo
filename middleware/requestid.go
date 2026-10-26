package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type requestIDKey struct{}

// requestIDHeader is the header RequestID reads an inbound id from and
// echoes the request's id in.
const requestIDHeader = "X-Request-Id"

// RequestID reads X-Request-Id or generates one, sets it on the response,
// and plants it in the context for handlers (and extractors) to read back
// with RequestIDFromContext. Recover and AccessLog put it on their lines as
// request_id. An inbound id is kept only when it is at most
// maxRequestIDLen bytes of printable ASCII without spaces; anything else is
// replaced, since the id is echoed into the response and into logs. Zero
// config.
type RequestID struct{}

// maxRequestIDLen bounds an inbound X-Request-Id: long enough for every
// common format (a UUID, a ULID, a trace id with a prefix), short enough
// that a client cannot pad logs with it.
const maxRequestIDLen = 128

// validRequestID accepts a non-empty id of at most maxRequestIDLen visible
// ASCII bytes — no spaces, controls or non-ASCII, none of which belong in a
// header value that is echoed back and logged.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func NewRequestID() *RequestID { return &RequestID{} }

func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			var buf [16]byte
			_, _ = rand.Read(buf[:]) // crypto/rand.Read never fails on supported platforms
			id = hex.EncodeToString(buf[:])
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFromContext returns the id RequestID planted, or "" outside it.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
