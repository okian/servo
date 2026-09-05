// Package mw holds the example's OWN middleware and extractor — the
// hand-written kind, demonstrating the seam itself. The commodity
// middleware (request id, recover, CORS) comes from servo's shipped
// middleware package instead; see cmd/app/spec.go.
package mw

import (
	"net/http"

	"github.com/okian/servo/v3/servo"
)

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
