package middleware

import (
	"bufio"
	"compress/gzip"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Gzip compresses responses for clients that accept gzip. The decision
// waits for the body: a response that ends, or is flushed, before
// gzipMinSize bytes goes out as it is, since compressing so little saves
// nothing on the wire; one that reaches it is compressed unless its content
// type, as set or as net/http would sniff it, is compressed already
// (images, audio, video, archives). Responses that already carry a
// Content-Encoding, whose status carries no body (1xx, 204, 304), or that
// carry a byte range (206, or any Content-Range) pass through untouched.
// Accept-Encoding is read with its weights, so "gzip;q=0" refuses gzip,
// and every response carries Vary: Accept-Encoding. Compressors are pooled.
// Zero config.
type Gzip struct{}

func NewGzip() *Gzip { return &Gzip{} }

func (m *Gzip) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The encoding depends on Accept-Encoding even for a client that
		// gets none, so a cache must key on it either way.
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r.Header.Values("Accept-Encoding")) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.release()
		next.ServeHTTP(gw, r)
		// Not deferred: after a panic the held body is dropped, leaving
		// Recover free to answer with a clean 500, and a compressed stream
		// is left without its trailer, so the client cannot mistake the
		// truncated body for a whole one.
		gw.finish()
	})
}

// gzipMinSize is the body size below which a response is sent as it is:
// it fits in one packet either way, and below a few dozen bytes gzip's own
// header and trailer make it bigger.
const gzipMinSize = 1024

// gzipWriters pools compressors. Each holds most of a megabyte of state,
// which a writer per response turned into that much garbage per request.
// Reset rebinds a pooled one to its next response, so one whose stream was
// abandoned midway is as good as new.
var gzipWriters = &sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// acceptsGzip reads Accept-Encoding with its weights, as RFC 9110 §12.5.3
// has them: gzip (or its alias x-gzip) is acceptable when listed with a
// non-zero weight, or when it is not listed and "*" is. "gzip;q=0" is a
// refusal, not a mention, and an element whose weight does not parse is
// ignored.
func acceptsGzip(values []string) bool {
	gzipQ, anyQ := -1.0, -1.0
	for _, v := range values {
		for elem := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(elem, ";")
			q, ok := weight(params)
			if !ok {
				continue
			}
			switch coding = strings.TrimSpace(coding); {
			case strings.EqualFold(coding, "gzip"), strings.EqualFold(coding, "x-gzip"):
				gzipQ = q
			case coding == "*":
				anyQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return anyQ > 0
}

// weight reads an Accept-Encoding element's q parameter: 1 when it has
// none, false when it is not a number from 0 to 1.
func weight(params string) (float64, bool) {
	for p := range strings.SplitSeq(params, ";") {
		name, value, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || !(q >= 0 && q <= 1) {
			return 0, false
		}
		return q, true
	}
	return 1, true
}

var incompressiblePrefixes = []string{"image/", "video/", "audio/"}

var incompressibleTypes = map[string]bool{
	"application/zip":  true,
	"application/gzip": true,
	"application/zstd": true,
}

func incompressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.TrimSpace(ct)
	for _, p := range incompressiblePrefixes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return incompressibleTypes[ct]
}

// passThrough reports whether a response goes out as it is whatever its
// size, judged from its status and the headers set with it.
func passThrough(code int, h http.Header) bool {
	switch {
	case code < 200, code == http.StatusNoContent, code == http.StatusNotModified:
		// No body: net/http refuses the bytes a gzip frame would write
		// to such a response, and a Content-Encoding on an empty body
		// misleads caches.
		return true
	case code == http.StatusPartialContent, h.Get("Content-Range") != "":
		// A range of the plain body: compressing it would make its
		// Content-Range describe bytes the client never receives.
		return true
	}
	return h.Get("Content-Encoding") != "" || incompressible(h.Get("Content-Type"))
}

// gzipWriter holds the final status and the first gzipMinSize bytes of
// body until they settle the encoding, then either compresses through a
// pooled gzip.Writer or passes everything through. Holding the status
// delays nothing a client could see: net/http itself sends no header
// before the handler returns, flushes, or outgrows its own buffer.
type gzipWriter struct {
	http.ResponseWriter
	code    int    // the final status, held until the decision; 0 until the handler sets one
	buf     []byte // the body, held until the decision
	decided bool   // the encoding is settled: gzip when gz is set, plain otherwise
	gz      *gzip.Writer
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.decided || g.code != 0 {
		return
	}
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		// An informational status (103 Early Hints) goes out at once;
		// the final one is still to come.
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.code = code
	if passThrough(code, g.Header()) {
		_ = g.startPlain()
	}
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided && g.code == 0 {
		g.WriteHeader(http.StatusOK)
	}
	switch {
	case g.gz != nil:
		return g.gz.Write(b)
	case g.decided:
		return g.ResponseWriter.Write(b)
	case len(g.buf)+len(b) < gzipMinSize:
		if g.buf == nil {
			g.buf = make([]byte, 0, gzipMinSize)
		}
		g.buf = append(g.buf, b...)
		return len(b), nil
	}
	// The body is big enough to be worth compressing: settle the encoding
	// with what is held, then send this write the way it was settled —
	// never copying a large write into the buffer first.
	if err := g.decide(b); err != nil {
		return 0, err
	}
	return g.Write(b)
}

// decide settles the encoding once the body has reached gzipMinSize: gzip,
// unless the body's type is compressed already. A handler that set no
// Content-Type gets the one net/http would sniff, set here from the plain
// bytes — net/http sniffs nothing once Content-Encoding is set, so a
// compressed response would otherwise go out with no type at all. next is
// the write that crossed the threshold, read only for sniffing.
func (g *gzipWriter) decide(next []byte) error {
	h := g.Header()
	if _, ok := h["Content-Type"]; !ok {
		h.Set("Content-Type", sniff(g.buf, next))
	}
	if incompressible(h.Get("Content-Type")) {
		return g.startPlain()
	}
	return g.startGzip()
}

// sniffLen is how much of a body http.DetectContentType reads.
const sniffLen = 512

// sniff detects the content type of the held body followed by next,
// copying only when the first sniffLen bytes straddle the two.
func sniff(held, next []byte) string {
	switch {
	case len(held) >= sniffLen:
		return http.DetectContentType(held)
	case len(held) == 0:
		return http.DetectContentType(next)
	}
	data := make([]byte, 0, sniffLen)
	data = append(data, held...)
	data = append(data, next[:min(len(next), sniffLen-len(held))]...)
	return http.DetectContentType(data)
}

// startPlain settles on sending the response as it is, starting with the
// held status and body.
func (g *gzipWriter) startPlain() error {
	g.decided = true
	if g.code != 0 {
		g.ResponseWriter.WriteHeader(g.code)
	}
	if len(g.buf) == 0 {
		return nil
	}
	_, err := g.ResponseWriter.Write(g.buf)
	g.buf = nil
	return err
}

// startGzip settles on compressing: the encoding header goes on, the plain
// length and Accept-Ranges come off, and the held body is the stream's
// first input. Ranges are served of the plain body only (a 206 passes
// through), so a client resuming a compressed download at an advertised
// range would splice plain bytes onto compressed ones.
func (g *gzipWriter) startGzip() error {
	g.decided = true
	h := g.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	g.ResponseWriter.WriteHeader(g.code)
	g.gz = gzipWriters.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
	_, err := g.gz.Write(g.buf)
	g.buf = nil
	return err
}

// finish completes the response of a handler that returned: one still
// undecided goes out as it is, a compressed one gets its trailer.
func (g *gzipWriter) finish() {
	switch {
	case g.gz != nil:
		_ = g.gz.Close()
	case !g.decided:
		_ = g.startPlain()
	}
}

// release returns the compressor to the pool, whether or not the handler
// returned.
func (g *gzipWriter) release() {
	if g.gz != nil {
		gzipWriters.Put(g.gz)
		g.gz = nil
	}
}

// Flush is http.Flusher's form of FlushError.
func (g *gzipWriter) Flush() { _ = g.FlushError() }

// FlushError sends what the handler has written so far. A response still
// under gzipMinSize is settled as plain: a handler flushing that early is
// streaming small pieces, and gzip's framing on every flush would cost more
// than it saves. A compressed one flushes the compressor first.
func (g *gzipWriter) FlushError() error {
	if !g.decided {
		if err := g.startPlain(); err != nil {
			return err
		}
	}
	if g.gz != nil {
		if err := g.gz.Flush(); err != nil {
			return err
		}
	}
	return http.NewResponseController(g.ResponseWriter).Flush()
}

// Hijack hands the connection to the handler — a websocket upgrade carries
// Accept-Encoding like any request — and nothing held is written after it.
func (g *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(g.ResponseWriter).Hijack()
	if err == nil {
		g.decided, g.buf = true, nil
	}
	return conn, rw, err
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
