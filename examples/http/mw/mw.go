// Package mw holds the example's middleware and extractor: a request-id
// middleware showing context mutation and a response header (pre/post), an
// auth middleware guarding the internal group, and a user extractor feeding
// a typed handler parameter.
package mw

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type ctxKey struct{}

// RequestID is server-level middleware: it runs for every group. It shows
// both directions — a response header set before the handler (post-visible
// to the client) and a context value the handler reads back out.
type RequestID struct{}

func NewRequestID() *RequestID { return &RequestID{} }

func (m *RequestID) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = "generated-1"
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// IDFromContext lets handlers (and the e2e test) read what the middleware
// planted — extractors and handlers both see the wrapped context.
func IDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Auth guards the internal group: everything on that listener needs the
// shared token, and the handlers never think about it.
type Auth struct{}

func NewAuth() *Auth { return &Auth{} }

func (m *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "letmein" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// User is the extracted type: any handler parameter of type *User is
// produced per request by UserExtractor.Extract.
type User struct{ Name string }

type UserExtractor struct{}

func NewUserExtractor() *UserExtractor { return &UserExtractor{} }

// Extract fails through the same status contract as a handler: a missing
// header is a 401 before the handler ever runs.
func (e *UserExtractor) Extract(r *http.Request) (*User, error) {
	name := r.Header.Get("X-User")
	if name == "" {
		return nil, servo.Status.UNAUTHORIZED.New("missing X-User header")
	}
	return &User{Name: name}, nil
}
