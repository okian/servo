// Package middleware is servo's batteries for the emitted HTTP servers:
// ordinary graph nodes with the standard
//
//	Middleware(next http.Handler) http.Handler
//
// method, attached in the spec with servo.Use[T](selector...) like any
// middleware you write yourself. The zero-config ones (Recover, AccessLog,
// RequestID, Gzip) need nothing beyond the Use line — their constructors
// take no arguments, so resolution finds them as they are. The configurable
// ones take a *XxxConfig node that **your own provider** supplies, the same
// pattern as *servo.HTTPConfig: the values are your module's business, and
// because the config is a real Go struct built in your code, fields like
// RateLimitConfig.KeyFunc are ordinary functions.
//
// Declaration order is wrap order, outermost first, and the shipped ones
// work best as RequestID, Recover, AccessLog, then the rest: every line
// Recover and AccessLog write then carries the request id, Recover sees
// every panic, and a request the rest refuse (a 429, a 413, a preflight) is
// still in the access log. Timeout belongs on the groups or routes whose
// responses are bounded, never ahead of a stream.
//
// Everything here is standard library only.
package middleware

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
)

// writeJSONError mirrors the error body the generated adapters produce, so
// a client sees one shape whether a middleware or a handler refused it. A
// Content-Length already on the header describes the body a handler meant
// to send, not this one — Recover writes after a handler that may have set
// it — so it goes, as http.Error drops it too.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}

// requestIDOf finds the request id a log line should carry: RequestID's
// context value when RequestID wraps the middleware writing the line, and
// otherwise the id RequestID set on the response. Either way the id is the
// one the client sees in X-Request-Id, whichever of the two runs first, as
// long as RequestID ran before the line is written.
func requestIDOf(w http.ResponseWriter, r *http.Request) string {
	if id := RequestIDFromContext(r.Context()); id != "" {
		return id
	}
	if id := w.Header().Get(requestIDHeader); validRequestID(id) {
		return id
	}
	return ""
}

// statusRecorder sits between a middleware and the handler it wraps to
// learn what the handler sent: the status, the body bytes, and whether the
// response has started, after which its status can no longer change.
//
// Handlers reach for the server's optional interfaces by type assertion —
// an event stream asserts http.Flusher, a websocket upgrade http.Hijacker,
// an HTTP/2 server push http.Pusher, and io.Copy looks for io.ReaderFrom to
// use sendfile — so a wrapper that only embedded http.ResponseWriter would
// quietly take them away. The recorder offers all four and reaches the
// writer underneath through http.ResponseController, however many wrappers
// deep it is; Unwrap does the same for the rest of ResponseController
// (deadlines, full duplex). Where the server's own writer lacks a feature
// (an HTTP/2 stream cannot be hijacked, an HTTP/1 one cannot push), the
// call fails with http.ErrNotSupported, as those interfaces allow.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	bytes   int64
	started bool
}

func (s *statusRecorder) WriteHeader(code int) {
	// An informational status (103 Early Hints) goes out ahead of the final
	// one, which is still to come. 101 is final, since nothing follows a
	// protocol switch; net/http draws the same line.
	informational := code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols
	if !s.started && !informational {
		s.status = code
		s.started = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.started = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Flush is http.Flusher's form of FlushError.
func (s *statusRecorder) Flush() { _ = s.FlushError() }

// FlushError flushes the writer underneath. A flush that succeeds has sent
// the header — a 200 when the handler set no status — so the response has
// started.
func (s *statusRecorder) FlushError() error {
	err := http.NewResponseController(s.ResponseWriter).Flush()
	if err == nil {
		s.started = true
	}
	return err
}

// Hijack hands the connection to the handler, after which nothing may be
// written through the response, so it counts as started.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err == nil {
		s.started = true
	}
	return conn, rw, err
}

// Push initiates an HTTP/2 server push through the writer underneath.
// http.ResponseController has no Push, so the wrappers are unwrapped here.
func (s *statusRecorder) Push(target string, opts *http.PushOptions) error {
	w := s.ResponseWriter
	for {
		if p, ok := w.(http.Pusher); ok {
			return p.Push(target, opts)
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return http.ErrNotSupported
		}
		w = u.Unwrap()
	}
}

// ReadFrom copies src into the writer underneath, through that writer's own
// ReadFrom when it has one.
func (s *statusRecorder) ReadFrom(src io.Reader) (int64, error) {
	n, err := io.Copy(s.ResponseWriter, src)
	if n > 0 {
		s.started = true
	}
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
