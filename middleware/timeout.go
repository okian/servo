package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// TimeoutConfig is provided by your own provider.
type TimeoutConfig struct {
	// Limit is how long a handler may run. Must be positive.
	Limit time.Duration
	// Body is the 503 payload; empty means the JSON error default. A body
	// that is valid JSON is sent as application/json; any other gets the
	// type net/http sniffs from it.
	Body string
}

// Timeout wraps http.TimeoutHandler: a handler exceeding the limit gets cut
// off with a 503 and the configured body, and its context is cancelled so
// well-behaved work stops. Cancelling is all it can do, and
// http.TimeoutHandler brings three limits with it:
//
//   - The whole response is buffered until the handler returns. Nothing
//     reaches the client before then, and a large body costs its full size
//     in memory.
//   - The handler's writer is neither an http.Flusher nor an http.Hijacker,
//     and http.ResponseController's Flush reports http.ErrNotSupported, so
//     a streaming handler cannot stream and a websocket cannot upgrade.
//   - A handler past its limit keeps running in its own goroutine until it
//     returns; its writes fail with http.ErrHandlerTimeout, but nothing
//     stops it. The server counts the request as finished once the 503 is
//     out, so srv.Shutdown does not wait for the handler either: one that
//     ignores its context can outlive Shutdown and reach dependencies that
//     have already stopped.
//
// So attach Timeout to the groups or routes whose responses are bounded,
// never ahead of a streaming one — a //servo: route returning servo.Stream,
// an event stream, a websocket. Bound those another way: their work through
// the request context (context.WithTimeout, or a reader that stops when the
// context ends), and slow clients through write deadlines —
// HTTPConfig.WriteTimeout for the whole listener, or
// http.ResponseController.SetWriteDeadline, renewed as the stream goes,
// from middleware that holds the ResponseWriter.
type Timeout struct {
	limit time.Duration
	body  string
	// contentType is set when body is JSON: http.TimeoutHandler writes its
	// 503 with no Content-Type, and net/http sniffs JSON as text/plain.
	contentType string
}

func NewTimeout(cfg *TimeoutConfig) (*Timeout, error) {
	if cfg.Limit <= 0 {
		return nil, errors.New("middleware: TimeoutConfig.Limit must be positive")
	}
	body := cfg.Body
	if body == "" {
		body = `{"error":"request timed out"}`
	}
	t := &Timeout{limit: cfg.Limit, body: body}
	if json.Valid([]byte(body)) {
		t.contentType = "application/json"
	}
	return t, nil
}

func (m *Timeout) Middleware(next http.Handler) http.Handler {
	th := http.TimeoutHandler(next, m.limit, m.body)
	if m.contentType == "" {
		return th
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &timeoutTypeWriter{ResponseWriter: w, body: m.body, contentType: m.contentType}
		th.ServeHTTP(tw, r)
		tw.release()
	})
}

// timeoutTypeWriter gives the 503 http.TimeoutHandler writes the
// Content-Type it never sets. The status alone cannot tell that 503 from
// one the handler returned itself, so a 503 without a Content-Type is held
// back until its body arrives: a body that is exactly the configured one
// gets the configured type, which is right whichever of them wrote it, and
// any other goes out as it came.
type timeoutTypeWriter struct {
	http.ResponseWriter
	body, contentType string
	held              bool // a 503 waiting for its first write
}

func (t *timeoutTypeWriter) WriteHeader(code int) {
	if _, typed := t.Header()["Content-Type"]; code == http.StatusServiceUnavailable && !typed {
		t.held = true
		return
	}
	t.ResponseWriter.WriteHeader(code)
}

func (t *timeoutTypeWriter) Write(b []byte) (int, error) {
	if t.held {
		t.held = false
		if string(b) == t.body {
			t.Header().Set("Content-Type", t.contentType)
		}
		t.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
	}
	return t.ResponseWriter.Write(b)
}

// release sends a held 503 that no body followed: http.TimeoutHandler
// writes it bare when the request's context ended for another reason than
// the limit, such as the client going away.
func (t *timeoutTypeWriter) release() {
	if t.held {
		t.held = false
		t.ResponseWriter.WriteHeader(http.StatusServiceUnavailable)
	}
}

func (t *timeoutTypeWriter) Unwrap() http.ResponseWriter { return t.ResponseWriter }
