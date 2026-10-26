package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a handler panic into a 500 with the canonical body, or
// aborts the response when the handler had already started it; the panic
// value and stack go to slog, never to the client. Zero config:
//
//	servo.Use[*middleware.Recover]()
//
// is the entire setup.
type Recover struct{}

func NewRecover() *Recover { return &Recover{} }

// Middleware recovers a panic in next and logs it at ERROR, with the
// request's context, its route and its request id when it has them, and the
// stack. A response that has not started gets the canonical 500. One that
// has — a status sent, part of a body written — can no longer be answered:
// the status is gone, and an error appended to the body would read as the
// end of a 200. Recover aborts it instead by panicking with
// http.ErrAbortHandler, which net/http turns into a dropped connection (a
// reset stream on HTTP/2), so the client sees the failure it is.
//
// http.ErrAbortHandler itself is net/http's sentinel for a deliberate abort
// — how a handler, the generated HTTP adapter included, cuts off a response
// that failed midway. It is a decision rather than a crash, so it passes
// through untouched and is not logged.
func (m *Recover) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			args := []any{"method", r.Method, "path", r.URL.Path}
			if r.Pattern != "" {
				args = append(args, "route", r.Pattern)
			}
			if id := requestIDOf(w, r); id != "" {
				args = append(args, "request_id", id)
			}
			args = append(args, "panic", v, "stack", string(debug.Stack()))
			slog.ErrorContext(r.Context(), "servo: handler panicked", args...)
			if rec.started {
				panic(http.ErrAbortHandler)
			}
			writeJSONError(w, http.StatusInternalServerError, "Internal Server Error")
		}()
		next.ServeHTTP(rec, r)
	})
}
