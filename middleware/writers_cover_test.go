package middleware

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// errConnGone is what a write to a connection the client dropped returns.
var errConnGone = errors.New("write: broken pipe")

// breakableRecorder is a ResponseRecorder whose connection breaks on cue:
// once broken, every body write fails, as a dead connection's do.
type breakableRecorder struct {
	*httptest.ResponseRecorder
	broken bool
}

func (b *breakableRecorder) Write(p []byte) (int, error) {
	if b.broken {
		return 0, errConnGone
	}
	return b.ResponseRecorder.Write(p)
}

// gzipOver serves handler through Gzip, to a client accepting gzip, on w.
func gzipOver(w http.ResponseWriter, handler http.HandlerFunc) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	NewGzip().Middleware(handler).ServeHTTP(w, req)
}

// The first final status is the response's, as net/http has it: a second
// WriteHeader — before the body, or once the encoding is settled — changes
// nothing, and the body still comes out compressed.
func TestGzipKeepsTheFirstStatus(t *testing.T) {
	w := httptest.NewRecorder()
	gzipOver(w, func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusCreated)
		rw.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(rw, big)
		rw.WriteHeader(http.StatusAccepted)
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("code = %d, want the first status, 201", w.Code)
	}
	if w.Header().Get("Content-Encoding") != "gzip" || gunzip(t, w.Body.Bytes()) != big {
		t.Fatalf("the body did not come out compressed whole")
	}
}

// A connection that broke is reported to the handler by the write or the
// flush that ran into it, whichever settles the encoding, so a handler
// streaming a response can stop rather than go on producing it.
func TestGzipReportsABrokenConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(rw http.ResponseWriter, conn *breakableRecorder) error
	}{
		{"the write that crosses the threshold", func(rw http.ResponseWriter, conn *breakableRecorder) error {
			conn.broken = true
			n, err := io.WriteString(rw, big)
			if n != 0 {
				return fmt.Errorf("wrote %d bytes to a broken connection", n)
			}
			return err
		}},
		{"a flush of a body under the threshold", func(rw http.ResponseWriter, conn *breakableRecorder) error {
			_, _ = io.WriteString(rw, "tiny")
			conn.broken = true
			return http.NewResponseController(rw).Flush()
		}},
		{"a flush of a compressed stream", func(rw http.ResponseWriter, conn *breakableRecorder) error {
			_, _ = io.WriteString(rw, big)
			conn.broken = true
			return http.NewResponseController(rw).Flush()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &breakableRecorder{ResponseRecorder: httptest.NewRecorder()}
			var err error
			gzipOver(conn, func(rw http.ResponseWriter, r *http.Request) { err = tc.handler(rw, conn) })
			if !errors.Is(err, errConnGone) {
				t.Fatalf("the handler got %v, want the connection's own error", err)
			}
		})
	}
}

// deadlineRecorder is a ResponseRecorder that can push and take a write
// deadline, as a server's HTTP/2 writer can.
type deadlineRecorder struct {
	pushRecorder
	deadline time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadline = t
	return nil
}

// A handler behind several wrappers still reaches what only the server's
// writer can do. A push walks down through each wrapper that is not a
// pusher itself, the gzip writer here; http.ResponseController's other
// calls, a write deadline renewed as a stream goes, unwrap the recorder
// and the gzip writer alike.
func TestWrappersReachTheServersWriter(t *testing.T) {
	captureLog(t)
	base := &deadlineRecorder{pushRecorder: pushRecorder{ResponseRecorder: httptest.NewRecorder()}}
	deadline := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var pushErr, deadlineErr error
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	NewGzip().Middleware(NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushErr = w.(http.Pusher).Push("/app.css", nil)
		deadlineErr = http.NewResponseController(w).SetWriteDeadline(deadline)
	}))).ServeHTTP(base, req)

	if pushErr != nil || len(base.pushed) != 1 || base.pushed[0] != "/app.css" {
		t.Errorf("push = %v, pushed %v: it must reach the server's writer under the gzip writer", pushErr, base.pushed)
	}
	if deadlineErr != nil || !base.deadline.Equal(deadline) {
		t.Errorf("SetWriteDeadline = %v, deadline %v: it must reach the server's writer", deadlineErr, base.deadline)
	}
}

// A limit so slow that its refill time overflows a time.Duration keeps
// every bucket for good, rather than wrapping round to a negative idle
// time that would forget a bucket at the first sweep and hand its client
// a token it has not earned.
func TestRateLimitIdleTimeSaturates(t *testing.T) {
	rl, h := rateLimited(t, &RateLimitConfig{RPS: 1e-10, Burst: 1})
	if rl.idle != time.Duration(math.MaxInt64) {
		t.Fatalf("idle = %v, want the longest Duration", rl.idle)
	}
	current := time.Now()
	rl.now = func() time.Time { return current }
	if w := hit(h, "10.0.0.1:1"); w.Code != http.StatusOK {
		t.Fatalf("first request = %d", w.Code)
	}
	current = current.Add(time.Hour)
	for i := range 10 {
		hit(h, fmt.Sprintf("10.1.0.%d:1", i))
	}
	if _, ok := rl.buckets["10.0.0.1"]; !ok {
		t.Fatalf("the bucket was forgotten an hour in; at this rate it needs centuries to refill")
	}
	if w := hit(h, "10.0.0.1:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("an hour later = %d, want 429", w.Code)
	}
}
