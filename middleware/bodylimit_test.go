package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodyLimitRejectsDeclaredOversize(t *testing.T) {
	bl, err := NewBodyLimit(&BodyLimitConfig{MaxBytes: 8})
	if err != nil {
		t.Fatalf("NewBodyLimit: %v", err)
	}
	reached := false
	h := bl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("way more than eight bytes"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", w.Code)
	}
	if reached {
		t.Fatalf("handler must not run for a declared-oversize body")
	}
}

// Without a Content-Length (chunked), the cap still guards the read path:
// the handler's read fails at the boundary instead of buffering forever.
func TestBodyLimitGuardsReads(t *testing.T) {
	bl, err := NewBodyLimit(&BodyLimitConfig{MaxBytes: 8})
	if err != nil {
		t.Fatalf("NewBodyLimit: %v", err)
	}
	var readErr error
	h := bl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("way more than eight bytes"))
	req.ContentLength = -1 // chunked
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatalf("reading past the cap must fail")
	}
}

func TestBodyLimitPassesSmallBodies(t *testing.T) {
	bl, err := NewBodyLimit(&BodyLimitConfig{MaxBytes: 64})
	if err != nil {
		t.Fatalf("NewBodyLimit: %v", err)
	}
	var got string
	h := bl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("ok")))
	if got != "ok" {
		t.Fatalf("body = %q", got)
	}
}

func TestBodyLimitValidatesConfig(t *testing.T) {
	if _, err := NewBodyLimit(&BodyLimitConfig{}); err == nil {
		t.Fatalf("MaxBytes must be positive")
	}
}
