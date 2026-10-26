package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// gunzip decodes a whole compressed body, failing the test if it is not
// one.
func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip stream: %v", err)
	}
	return string(plain)
}

// big is a compressible body comfortably over gzipMinSize.
var big = strings.Repeat("servo ", 400)

func TestGzipCompressesWhenAccepted(t *testing.T) {
	body := strings.Repeat("servo ", 200)
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte(body))
	})

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q", got)
	}
	if got := gunzip(t, w.Body.Bytes()); got != body {
		t.Fatalf("round-trip failed: %d bytes back, want %d", len(got), len(body))
	}
	if w.Body.Len() >= len(body) {
		t.Fatalf("compressed body (%d) not smaller than plain (%d)", w.Body.Len(), len(body))
	}
	if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q", got)
	}
}

// A client that does not take gzip still gets Vary: a cache must not hand
// it the compressed copy another client caused to be stored.
func TestGzipSkipsWithoutAccept(t *testing.T) {
	w := gzipped(t, "", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(big))
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Fatalf("encoding=%q body=%d bytes", w.Header().Get("Content-Encoding"), w.Body.Len())
	}
	if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want it on the plain response too", got)
	}
}

// Below gzipMinSize a body goes out as it is: compressing two bytes made
// them 27.
func TestGzipLeavesSmallBodiesAlone(t *testing.T) {
	for _, n := range []int{0, 2, gzipMinSize - 1} {
		body := strings.Repeat("a", n)
		w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(body))
		})
		if w.Header().Get("Content-Encoding") != "" || w.Body.String() != body {
			t.Fatalf("%d bytes: encoding=%q body=%d bytes, want it plain", n, w.Header().Get("Content-Encoding"), w.Body.Len())
		}
	}
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = rw.Write([]byte(strings.Repeat("a", gzipMinSize)))
	})
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%d bytes went out plain; the threshold compresses from there", gzipMinSize)
	}
}

// A small body keeps the Content-Length its handler set; a compressed one
// loses it, since it no longer describes the bytes sent.
func TestGzipContentLength(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Length", "2")
		_, _ = rw.Write([]byte("ok"))
	})
	if got := w.Header().Get("Content-Length"); got != "2" {
		t.Fatalf("small body: Content-Length = %q, want the handler's 2", got)
	}
	w = gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Length", "2400")
		_, _ = rw.Write([]byte(big))
	})
	if got := w.Header().Get("Content-Length"); got != "" || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("compressed body: Content-Length = %q, encoding %q", got, w.Header().Get("Content-Encoding"))
	}
}

// Accept-Encoding is read with its weights: q=0 is a refusal, not a
// mention.
func TestGzipHonoursWeights(t *testing.T) {
	for _, tc := range []struct {
		accept []string
		want   bool
	}{
		{[]string{"gzip"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip; q=0.000"}, false},
		{[]string{"GZIP;Q=0.5"}, true},
		{[]string{"deflate, gzip;q=0"}, false},
		{[]string{"x-gzip"}, true},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"gzip;q=0, *"}, false},
		{[]string{"br, *;q=0.1"}, true},
		{[]string{"identity"}, false},
		{[]string{"gzip;q=2"}, false},
		{[]string{"identity", "gzip"}, true},
	} {
		h := NewGzip().Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(big))
		}))
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		for _, v := range tc.accept {
			req.Header.Add("Accept-Encoding", v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("Content-Encoding") == "gzip"; got != tc.want {
			t.Errorf("Accept-Encoding %q: compressed = %v, want %v", tc.accept, got, tc.want)
		}
	}
}

// Many small writes are held until they add up to the threshold, then
// compressed as one stream.
func TestGzipCompressesManySmallWrites(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		for range 100 {
			_, _ = io.WriteString(rw, "twenty bytes, give o")
		}
	})
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("2000 bytes in 20-byte writes went out plain")
	}
	if got := gunzip(t, w.Body.Bytes()); got != strings.Repeat("twenty bytes, give o", 100) {
		t.Fatalf("round-trip lost bytes: %d back", len(got))
	}
}

// A handler that sets no Content-Type gets the one net/http would sniff:
// net/http sniffs nothing once Content-Encoding is set, and a sniffed type
// that is compressed already is not compressed again.
func TestGzipSniffsContentType(t *testing.T) {
	html := "<!DOCTYPE html><html><body>" + big + "</body></html>"
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, html[:100]) // held
		_, _ = io.WriteString(rw, html[100:]) // crosses the threshold
	})
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("encoding %q, type %q", w.Header().Get("Content-Encoding"), w.Header().Get("Content-Type"))
	}
	if gunzip(t, w.Body.Bytes()) != html {
		t.Fatalf("round-trip failed")
	}

	png := "\x89PNG\x0D\x0A\x1A\x0A" + big
	w = gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, png)
	})
	if w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != png {
		t.Fatalf("PNG: encoding %q, type %q", w.Header().Get("Content-Encoding"), w.Header().Get("Content-Type"))
	}
}

// Already-compressed content types pass through untouched.
func TestGzipSkipsIncompressibleTypes(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "image/png")
		_, _ = rw.Write([]byte(big))
	})
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Fatalf("encoding=%q body=%d bytes", w.Header().Get("Content-Encoding"), w.Body.Len())
	}
}

func TestGzipSkipsPreEncodedResponses(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Encoding", "br")
		_, _ = rw.Write([]byte(big))
	})
	if got := w.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want the handler's own", got)
	}
	if w.Body.String() != big {
		t.Fatalf("body changed: %d bytes", w.Body.Len())
	}
}

// Statuses that carry no body get no gzip frame and no encoding header.
func TestGzipSkipsBodylessStatuses(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotModified} {
		w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
			rw.WriteHeader(code)
		})
		if w.Code != code || w.Header().Get("Content-Encoding") != "" || w.Body.Len() != 0 {
			t.Fatalf("%d: code=%d encoding=%q body=%d bytes", code, w.Code, w.Header().Get("Content-Encoding"), w.Body.Len())
		}
	}
}

// A byte range is a range of the plain body; compressed, its Content-Range
// would describe bytes the client never receives. And a compressed
// response stops advertising ranges, which are served of the plain body
// only.
func TestGzipLeavesRangesAlone(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Range", "bytes 0-2399/9000")
		rw.WriteHeader(http.StatusPartialContent)
		_, _ = rw.Write([]byte(big))
	})
	if w.Code != http.StatusPartialContent || w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Fatalf("206: code=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
	}

	w = gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Accept-Ranges", "bytes")
		_, _ = rw.Write([]byte(big))
	})
	if w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("compressed: encoding=%q Accept-Ranges=%q", w.Header().Get("Content-Encoding"), w.Header().Get("Accept-Ranges"))
	}
}

// A 103 Early Hints goes out at once; the final status still decides.
func TestGzipPassesInformationalStatus(t *testing.T) {
	h := NewGzip().Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusEarlyHints)
		rw.WriteHeader(http.StatusCreated)
		_, _ = rw.Write([]byte(big))
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := newHintsRecorder()
	h.ServeHTTP(w, req)
	if len(w.informational) != 1 || w.Code != http.StatusCreated || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("hints %v, code %d, encoding %q", w.informational, w.Code, w.Header().Get("Content-Encoding"))
	}
}

// A handler flushing before the threshold is streaming small pieces: the
// response is settled as plain and the flush reaches the server.
func TestGzipFlushBeforeThresholdSendsPlain(t *testing.T) {
	w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(rw, "data: 1\n\n")
		rw.(http.Flusher).Flush()
		_, _ = io.WriteString(rw, "data: 2\n\n")
	})
	if !w.Flushed || w.Header().Get("Content-Encoding") != "" || w.Body.String() != "data: 1\n\ndata: 2\n\n" {
		t.Fatalf("flushed=%v encoding=%q body=%q", w.Flushed, w.Header().Get("Content-Encoding"), w.Body.String())
	}
}

// A flush in a compressed stream pushes the compressor's pending output
// out, so the client can decode everything written so far.
func TestGzipFlushesCompressedStream(t *testing.T) {
	w := httptest.NewRecorder()
	var atFlush []byte
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	NewGzip().Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, big)
		if err := http.NewResponseController(rw).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
		atFlush = bytes.Clone(w.Body.Bytes())
		_, _ = io.WriteString(rw, "tail")
	})).ServeHTTP(w, req)

	zr, err := gzip.NewReader(bytes.NewReader(atFlush))
	if err != nil {
		t.Fatalf("flushed prefix is not gzip: %v", err)
	}
	got := make([]byte, len(big))
	if _, err := io.ReadFull(zr, got); err != nil || string(got) != big {
		t.Fatalf("decoding the flushed prefix: %v", err)
	}
	if gunzip(t, w.Body.Bytes()) != big+"tail" {
		t.Fatalf("the whole stream does not round-trip")
	}
}

// Handlers assert http.Flusher for event streams and http.Hijacker for
// websockets; the gzip writer keeps both, and writes nothing after a
// hijack.
func TestGzipKeepsFlusherAndHijacker(t *testing.T) {
	rec := newHijackRecorder(t)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	NewGzip().Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if _, ok := rw.(http.Flusher); !ok {
			t.Fatalf("handler lost http.Flusher")
		}
		hj, ok := rw.(http.Hijacker)
		if !ok {
			t.Fatalf("handler lost http.Hijacker")
		}
		_, _ = io.WriteString(rw, "held, then dropped")
		if _, _, err := hj.Hijack(); err != nil {
			t.Fatalf("Hijack: %v", err)
		}
	})).ServeHTTP(rec, req)
	if !rec.hijacked || rec.writesAfter != 0 {
		t.Fatalf("hijacked=%v, %d writes after the hijack", rec.hijacked, rec.writesAfter)
	}
}

// Compressors come from a pool: a writer per response made most of a
// megabyte of garbage per request. Under the race detector the pool drops
// a quarter of what it is given, so the bound leaves room for that.
func TestGzipPoolsWriters(t *testing.T) {
	made := 0
	old := gzipWriters
	gzipWriters = &sync.Pool{New: func() any { made++; return gzip.NewWriter(nil) }}
	t.Cleanup(func() { gzipWriters = old })

	for range 100 {
		w := gzipped(t, "gzip", func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(big))
		})
		if gunzip(t, w.Body.Bytes()) != big {
			t.Fatalf("a reused compressor produced a different stream")
		}
	}
	if made > 50 {
		t.Fatalf("%d compressors for 100 responses; they must be reused", made)
	}
}

// A panic before the decision drops the held body, so Recover outside can
// still answer with a clean 500 rather than one glued to half a page.
func TestGzipPanicLeavesTheResponseToRecover(t *testing.T) {
	captureLog(t)
	h := NewRecover().Middleware(NewGzip().Middleware(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(rw, "<p>half a page")
		panic("boom")
	})))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError || w.Header().Get("Content-Encoding") != "" || strings.Contains(w.Body.String(), "half a page") {
		t.Fatalf("code=%d encoding=%q body=%q", w.Code, w.Header().Get("Content-Encoding"), w.Body.String())
	}
}
