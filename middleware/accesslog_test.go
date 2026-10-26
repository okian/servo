package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogEmitsOneLine(t *testing.T) {
	logs := captureLog(t)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("12345"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/orders", nil))

	line := logs.String()
	if n := strings.Count(line, "http request"); n != 1 {
		t.Fatalf("%d lines, want one: %s", n, line)
	}
	for _, want := range []string{"method=POST", "path=/orders", "status=201", "bytes=5"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q: %s", want, line)
		}
	}
	// No mux, no RequestID, no panic: nothing to say about any of them.
	for _, absent := range []string{"route=", "request_id=", "panicked=", "aborted="} {
		if strings.Contains(line, absent) {
			t.Errorf("log line has %q: %s", absent, line)
		}
	}
}

// A handler that never calls WriteHeader logged as the 200 it implicitly is.
func TestAccessLogImplicit200(t *testing.T) {
	logs := captureLog(t)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("log line: %s", logs)
	}
}

// The line is written in a defer. With Recover outermost, as in every
// example, a panicking request used to vanish from the access log; it is
// logged as the 500 Recover answers, and the panic still reaches Recover.
func TestAccessLogLogsPanickingRequests(t *testing.T) {
	logs := captureLog(t)

	h := NewRecover().Middleware(NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	line := lineWith(logs, "http request")
	for _, want := range []string{"status=500", "panicked=true"} {
		if !strings.Contains(line, want) {
			t.Errorf("access line missing %q: %q", want, line)
		}
	}
	if w.Code != http.StatusInternalServerError || lineWith(logs, "handler panicked") == "" {
		t.Fatalf("code %d; Recover must still see the panic and answer it: %s", w.Code, logs)
	}
}

// A handler that panics after writing is logged with the status it sent,
// and the panic passes through with its own value.
func TestAccessLogPanicAfterWrite(t *testing.T) {
	logs := captureLog(t)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("12"))
		panic("boom")
	}))
	if v := panicOf(func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}); v != "boom" {
		t.Fatalf("recovered %v: AccessLog must let the panic through", v)
	}
	for _, want := range []string{"status=201", "bytes=2", "panicked=true"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log line missing %q: %s", want, logs)
		}
	}
}

// A response cut off on purpose is an abort, not a crash: marked as such,
// and the sentinel passes through untouched so net/http drops the
// connection quietly.
func TestAccessLogMarksAbortedResponses(t *testing.T) {
	logs := captureLog(t)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic(http.ErrAbortHandler)
	}))
	if v := panicOf(func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	}); v != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want ErrAbortHandler itself", v)
	}
	line := lineWith(logs, "http request")
	if !strings.Contains(line, "status=200") || !strings.Contains(line, "aborted=true") || strings.Contains(line, "panicked=") {
		t.Fatalf("access line %q, want the 200 it sent, marked aborted", line)
	}
}

// With RequestID outside and the mux inside, the line carries the request
// id and the route that served the request.
func TestAccessLogRequestIDAndRoute(t *testing.T) {
	logs := captureLog(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {})
	h := NewRequestID().Middleware(NewAccessLog().Middleware(mux))
	req := httptest.NewRequest(http.MethodGet, "/items/42", nil)
	req.Header.Set("X-Request-Id", "req-42")
	h.ServeHTTP(httptest.NewRecorder(), req)

	for _, want := range []string{"path=/items/42", `route="GET /items/{id}"`, "request_id=req-42"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log line missing %s: %s", want, logs)
		}
	}
}

// AccessLog outside RequestID still logs the id, from the response header
// RequestID set — the client sees the same one.
func TestAccessLogRequestIDFromInside(t *testing.T) {
	logs := captureLog(t)

	h := NewAccessLog().Middleware(NewRequestID().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	id := w.Header().Get("X-Request-Id")
	if id == "" || !strings.Contains(logs.String(), "request_id="+id) {
		t.Fatalf("response id %q, log: %s", id, logs)
	}
}

// The line is logged under the request's context, for handlers that read
// it (trace ids).
func TestAccessLogLogsUnderRequestContext(t *testing.T) {
	ctxs := captureContexts(t)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), markerKey{}, "outer"))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(*ctxs) != 1 || (*ctxs)[0].Value(markerKey{}) != "outer" {
		t.Fatalf("%d records; the line must be logged with the request's context", len(*ctxs))
	}
}
