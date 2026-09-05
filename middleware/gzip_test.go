package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipped(t *testing.T, accept string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	h := NewGzip().Middleware(handler)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestGzipCompressesWhenAccepted(t *testing.T) {
	body := strings.Repeat("servo ", 200)
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte(body))
	})

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || string(plain) != body {
		t.Fatalf("round-trip failed: %v", err)
	}
	if w.Body.Len() >= len(body) {
		t.Fatalf("compressed body (%d) not smaller than plain (%d)", w.Body.Len(), len(body))
	}
}

func TestGzipSkipsWithoutAccept(t *testing.T) {
	w := gzipped(t, "", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte("plain"))
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != "plain" {
		t.Fatalf("encoding=%q body=%q", w.Header().Get("Content-Encoding"), w.Body.String())
	}
}

// Already-compressed content types pass through untouched.
func TestGzipSkipsIncompressibleTypes(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		_, _ = rw.Write([]byte("PNG-ish bytes"))
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != "PNG-ish bytes" {
		t.Fatalf("encoding=%q body=%q", w.Header().Get("Content-Encoding"), w.Body.String())
	}
}

func TestGzipSkipsPreEncodedResponses(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Encoding", "br")
		_, _ = rw.Write([]byte("already-encoded"))
	})
	if got := w.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want the handler's own", got)
	}
	if w.Body.String() != "already-encoded" {
		t.Fatalf("body = %q", w.Body.String())
	}
}
