package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a handler panic into a 500 with the canonical body — the
// panic value and stack go to slog, never to the client. Zero config:
//
//	servo.Use[*middleware.Recover]()
//
// is the entire setup.
type Recover struct{}

func NewRecover() *Recover { return &Recover{} }

func (m *Recover) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// net/http's own sentinel for "abort this connection" must keep
			// propagating — swallowing it would turn a deliberate teardown
			// into a half-written 500.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			slog.Error("servo: handler panicked", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
			writeJSONError(w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(w, r)
	})
}
