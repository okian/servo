package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessLogEmitsOneLine(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("12345"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/orders", nil))

	line := buf.String()
	for _, want := range []string{"method=POST", "path=/orders", "status=201", "bytes=5"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q: %s", want, line)
		}
	}
}

// A handler that never calls WriteHeader logged as the 200 it implicitly is.
func TestAccessLogImplicit200(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	h := NewAccessLog().Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(buf.String(), "status=200") {
		t.Fatalf("log line: %s", buf.String())
	}
}
