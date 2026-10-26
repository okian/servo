package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverTurnsPanicInto500(t *testing.T) {
	captureLog(t)
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom: secret detail")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if got := w.Body.String(); !strings.Contains(got, "Internal Server Error") || strings.Contains(got, "secret detail") {
		t.Fatalf("body = %q — canonical text only, never the panic value", got)
	}
}

func TestRecoverPassesThrough(t *testing.T) {
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusTeapot {
		t.Fatalf("code = %d", w.Code)
	}
}

// net/http's own abort sentinel must keep panicking — it is how a handler
// deliberately tears a connection down, and swallowing it breaks that. It
// is a decision, not a crash, so nothing is logged.
func TestRecoverReraisesAbortHandler(t *testing.T) {
	logs := captureLog(t)
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	v := panicOf(func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	})
	if v != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want ErrAbortHandler to propagate", v)
	}
	if logs.Len() != 0 {
		t.Fatalf("an abort was logged as a crash: %s", logs)
	}
}

// After a partial write the status is gone, and the error body appended to
// the 200 would read as its end: Recover aborts the response instead,
// through the sentinel net/http turns into a dropped connection, and still
// logs the panic.
func TestRecoverAbortsStartedResponse(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"partial body": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"partial":`))
			panic("boom")
		},
		"status only": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			panic("boom")
		},
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			w := httptest.NewRecorder()
			v := panicOf(func() {
				NewRecover().Middleware(handler).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
			})
			if v != http.ErrAbortHandler {
				t.Fatalf("recovered %v, want ErrAbortHandler", v)
			}
			if strings.Contains(w.Body.String(), "Internal Server Error") {
				t.Fatalf("body = %q: nothing may be appended to a started response", w.Body.String())
			}
			if !strings.Contains(logs.String(), "panic=boom") {
				t.Fatalf("the panic must still be logged: %s", logs)
			}
		})
	}
}

// End to end: the client of a response that panicked midway sees a broken
// response, never a complete-looking 200 with an error glued to its end.
func TestRecoverAbortReachesTheClient(t *testing.T) {
	captureLog(t)
	srv := httptest.NewServer(NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[1,2,`))
		w.(http.Flusher).Flush()
		panic("boom")
	})))
	defer srv.Close()

	res, err := http.Get(srv.URL)
	if err != nil {
		return // the connection dropped before the header: also a visible failure
	}
	defer func() { _ = res.Body.Close() }()
	b, readErr := io.ReadAll(res.Body)
	body := string(b)
	if readErr == nil {
		t.Fatalf("status %d, body %q read to a clean end; the abort must surface as an error", res.StatusCode, body)
	}
	if strings.Contains(body, "Internal Server Error") {
		t.Fatalf("body = %q: the 500 was appended to the 200", body)
	}
}

// A 103 Early Hints does not start the response: the final status is still
// open, so a panic after one still gets the clean 500.
func TestRecoverAnswersAfterInformationalStatus(t *testing.T) {
	captureLog(t)
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		panic("boom")
	}))
	w := newHintsRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusInternalServerError || len(w.informational) != 1 {
		t.Fatalf("code = %d after hints %v, want the 500", w.Code, w.informational)
	}
}

// A Content-Length the handler set describes a body it never wrote; left
// on the 500 it would cut the JSON short.
func TestRecoverDropsStaleContentLength(t *testing.T) {
	captureLog(t)
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5")
		panic("boom")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if got := w.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q on the 500", got)
	}
}

// The crash is logged under the request's context, for handlers that read
// it (trace ids).
func TestRecoverLogsUnderRequestContext(t *testing.T) {
	ctxs := captureContexts(t)
	h := NewRecover().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), markerKey{}, "outer"))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(*ctxs) != 1 || (*ctxs)[0].Value(markerKey{}) != "outer" {
		t.Fatalf("%d records; the crash must be logged with the request's context", len(*ctxs))
	}
}

// The crash line carries the request id whichever of RequestID and Recover
// runs first, and the route the mux matched when Recover can see it.
func TestRecoverLogsRequestIDAndRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	for name, stack := range map[string]http.Handler{
		"RequestID first": NewRequestID().Middleware(NewRecover().Middleware(mux)),
		"Recover first":   NewRecover().Middleware(NewRequestID().Middleware(mux)),
	} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			req := httptest.NewRequest(http.MethodGet, "/items/7", nil)
			req.Header.Set("X-Request-Id", "req-7")
			w := httptest.NewRecorder()
			stack.ServeHTTP(w, req)

			line := lineWith(logs, "handler panicked")
			if w.Code != http.StatusInternalServerError || !strings.Contains(line, "request_id=req-7") {
				t.Fatalf("code %d, crash line without the request id: %q", w.Code, line)
			}
			// Only an unreplaced request carries the mux's pattern back out.
			if name == "RequestID first" && !strings.Contains(line, `route="GET /items/{id}"`) {
				t.Fatalf("crash line without the route: %q", line)
			}
		})
	}
}
