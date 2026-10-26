package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// AccessLog emits one slog line per request: method, path, route, status,
// body bytes, duration, remote and request id. Zero config — it logs
// through slog.Default() at call time, with the request's context, so it
// follows whatever logger main installs and whatever a context-aware
// handler adds (a trace id).
//
// The line is written in a defer, so a request whose handler panicked is
// logged too, marked panicked=true, before the panic carries on to Recover.
// Its status is the one the handler sent, or 500 if it sent none: what
// Recover answers when it wraps AccessLog. A response cut off on purpose
// with http.ErrAbortHandler is marked aborted=true instead.
//
// The route is r.Pattern, the pattern net/http's ServeMux matched, which
// the mux sets on the request it is handed. It is there when nothing
// between AccessLog and the mux replaced the request — RequestID and
// Timeout do, as does any middleware planting a context value — and absent
// when no route matched. The request id is RequestID's, from the context
// when RequestID runs first and from the response header otherwise.
type AccessLog struct{}

func NewAccessLog() *AccessLog { return &AccessLog{} }

func (m *AccessLog) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		returned := false
		defer func() {
			if returned {
				logRequest(w, r, rec, rec.status, time.Since(start), "")
				return
			}
			// next did not return: a panic is passing through, or the
			// handler called runtime.Goexit. Recover answers a response
			// that has not started with a 500 and cuts off one that has.
			v := recover()
			status := rec.status
			if !rec.started {
				status = http.StatusInternalServerError
			}
			mark := "panicked"
			if v == http.ErrAbortHandler {
				mark = "aborted"
			}
			logRequest(w, r, rec, status, time.Since(start), mark)
			if v != nil {
				panic(v)
			}
		}()
		next.ServeHTTP(rec, r)
		returned = true
	})
}

// logRequest writes the access line; mark, when set, is the key of a true
// attribute saying how the request ended.
func logRequest(w http.ResponseWriter, r *http.Request, rec *statusRecorder, status int, took time.Duration, mark string) {
	args := make([]any, 0, 20)
	args = append(args, "method", r.Method, "path", r.URL.Path)
	if r.Pattern != "" {
		args = append(args, "route", r.Pattern)
	}
	args = append(args,
		"status", status,
		"bytes", rec.bytes,
		"duration", took,
		"remote", r.RemoteAddr,
	)
	if id := requestIDOf(w, r); id != "" {
		args = append(args, "request_id", id)
	}
	if mark != "" {
		args = append(args, mark, true)
	}
	slog.InfoContext(r.Context(), "http request", args...)
}
