package middleware

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog points slog.Default at a text handler writing into the
// returned buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// lineWith returns the first logged line containing msg, "" if none does.
func lineWith(logs *bytes.Buffer, msg string) string {
	for l := range strings.Lines(logs.String()) {
		if strings.Contains(l, msg) {
			return l
		}
	}
	return ""
}

// panicOf runs f and returns what it panicked with, nil if it returned.
func panicOf(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

// hintsRecorder is an httptest.ResponseRecorder that takes informational
// statuses the way net/http does — sent ahead, not final — where the
// recorder itself would take a 103 as the response's status.
type hintsRecorder struct {
	*httptest.ResponseRecorder
	informational []int
}

func newHintsRecorder() *hintsRecorder {
	return &hintsRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (h *hintsRecorder) WriteHeader(code int) {
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		h.informational = append(h.informational, code)
		return
	}
	h.ResponseRecorder.WriteHeader(code)
}

// hijackRecorder is an httptest.ResponseRecorder whose connection can be
// taken over, as net/http's HTTP/1 writer's can, and which counts what is
// written through the response afterwards — net/http refuses all of it.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	hijacked      bool
	writesAfter   int
	peer, handler net.Conn
}

func newHijackRecorder(t *testing.T) *hijackRecorder {
	h := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.peer, h.handler = net.Pipe()
	t.Cleanup(func() { _ = h.peer.Close(); _ = h.handler.Close() })
	return h
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return h.handler, bufio.NewReadWriter(bufio.NewReader(h.handler), bufio.NewWriter(h.handler)), nil
}

func (h *hijackRecorder) WriteHeader(code int) {
	if h.hijacked {
		h.writesAfter++
	}
	h.ResponseRecorder.WriteHeader(code)
}

func (h *hijackRecorder) Write(b []byte) (int, error) {
	if h.hijacked {
		h.writesAfter++
	}
	return h.ResponseRecorder.Write(b)
}

// ctxHandler is a slog.Handler that keeps the context each record was
// logged under: what a context-aware handler, a tracing bridge say, reads.
type ctxHandler struct{ ctxs *[]context.Context }

func (h ctxHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h ctxHandler) Handle(ctx context.Context, _ slog.Record) error {
	*h.ctxs = append(*h.ctxs, ctx)
	return nil
}
func (h ctxHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h ctxHandler) WithGroup(string) slog.Handler      { return h }

// captureContexts points slog.Default at a ctxHandler for the rest of the
// test.
func captureContexts(t *testing.T) *[]context.Context {
	t.Helper()
	var ctxs []context.Context
	old := slog.Default()
	slog.SetDefault(slog.New(ctxHandler{&ctxs}))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &ctxs
}

type markerKey struct{}

// Recover and AccessLog wrap the writer to learn what the handler wrote,
// and must not take away what the server's own writer offers: handlers
// assert http.Flusher for event streams, http.Hijacker for websockets, and
// io.Copy looks for io.ReaderFrom.
func TestRecorderKeepsOptionalInterfaces(t *testing.T) {
	captureLog(t)
	for name, wrap := range map[string]func(http.Handler) http.Handler{
		"Recover":   NewRecover().Middleware,
		"AccessLog": NewAccessLog().Middleware,
	} {
		t.Run(name, func(t *testing.T) {
			rec := newHijackRecorder(t)
			wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rf, ok := w.(io.ReaderFrom)
				if !ok {
					t.Fatalf("handler lost io.ReaderFrom")
				}
				// A bare reader, so the copy cannot go through a WriteTo.
				if n, err := rf.ReadFrom(struct{ io.Reader }{strings.NewReader("streamed")}); n != 8 || err != nil {
					t.Errorf("ReadFrom = %d, %v", n, err)
				}
				f, ok := w.(http.Flusher)
				if !ok {
					t.Fatalf("handler lost http.Flusher")
				}
				f.Flush()
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("ResponseController.Flush: %v", err)
				}
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatalf("handler lost http.Hijacker")
				}
				if _, _, err := hj.Hijack(); err != nil {
					t.Errorf("Hijack: %v", err)
				}
			})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if rec.Body.String() != "streamed" || !rec.Flushed || !rec.hijacked {
				t.Fatalf("body=%q flushed=%v hijacked=%v: the calls must reach the server's writer", rec.Body.String(), rec.Flushed, rec.hijacked)
			}
			if rec.writesAfter != 0 {
				t.Fatalf("%d writes after the hijack", rec.writesAfter)
			}
		})
	}
}

// pushRecorder is an httptest.ResponseRecorder that can push, as an HTTP/2
// writer can.
type pushRecorder struct {
	*httptest.ResponseRecorder
	pushed []string
}

func (p *pushRecorder) Push(target string, _ *http.PushOptions) error {
	p.pushed = append(p.pushed, target)
	return nil
}

// Recover used to hand the handler the server's own writer, http.Pusher
// included; wrapped twice over, a push still reaches the writer that can
// make it, and one that cannot says so the way the interface documents.
func TestRecorderPushes(t *testing.T) {
	captureLog(t)
	stack := func(handler http.HandlerFunc) http.Handler {
		return NewAccessLog().Middleware(NewRecover().Middleware(handler))
	}
	var err error
	push := func(w http.ResponseWriter, r *http.Request) {
		p, ok := w.(http.Pusher)
		if !ok {
			t.Fatalf("handler lost http.Pusher")
		}
		err = p.Push("/app.css", nil)
	}

	rec := &pushRecorder{ResponseRecorder: httptest.NewRecorder()}
	stack(push).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if err != nil || len(rec.pushed) != 1 || rec.pushed[0] != "/app.css" {
		t.Fatalf("push = %v, pushed %v: it must reach the server's writer", err, rec.pushed)
	}

	stack(push).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("push without a pusher underneath = %v, want http.ErrNotSupported", err)
	}
}

// A 103 Early Hints goes out ahead of the final status, which is still to
// come: it neither starts the response nor counts as its status.
func TestRecorderIgnoresInformationalStatus(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: newHintsRecorder(), status: http.StatusOK}
	rec.WriteHeader(http.StatusEarlyHints)
	if rec.started {
		t.Fatalf("a 103 started the response")
	}
	rec.WriteHeader(http.StatusCreated)
	if !rec.started || rec.status != http.StatusCreated {
		t.Fatalf("started=%v status=%d, want the final 201", rec.started, rec.status)
	}
}

// The request id a line carries is RequestID's from the context, or the
// one it set on the response when it runs inside the logging middleware —
// never an unvalidated value from anywhere else.
func TestRequestIDOf(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("X-Request-Id", "client-sent")
	w := httptest.NewRecorder()
	if got := requestIDOf(w, r); got != "" {
		t.Fatalf("without RequestID: %q, want none (the inbound header is not validated)", got)
	}
	w.Header().Set("X-Request-Id", "from-response")
	if got := requestIDOf(w, r); got != "from-response" {
		t.Fatalf("from the response: %q", got)
	}
	ctx := r.WithContext(context.WithValue(r.Context(), requestIDKey{}, "from-context"))
	if got := requestIDOf(w, ctx); got != "from-context" {
		t.Fatalf("from the context: %q, which wins", got)
	}
	w.Header().Set("X-Request-Id", "has space")
	if got := requestIDOf(w, r); got != "" {
		t.Fatalf("an invalid response id was logged: %q", got)
	}
}
