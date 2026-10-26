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

// NoSniff is middleware in the shape most of the ecosystem ships —
// otelhttp.NewMiddleware's result, chi's middleware, gorilla/handlers — a
// plain func(http.Handler) http.Handler. servo.Use attaches it as it is:
// the generated chain calls the node itself where a struct would need a
// Middleware method, so none of those packages needs a wrapper type. The
// named type is what gives the node a provider; a func type of your own
// around a third-party function works the same way.
type NoSniff func(http.Handler) http.Handler

func NewNoSniff() NoSniff {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
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
