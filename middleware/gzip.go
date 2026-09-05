package middleware

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// Gzip compresses responses for clients that accept it. The decision is
// made at WriteHeader time, when the handler's own headers are known:
// responses that already carry a Content-Encoding, or whose content type is
// inherently compressed (images, audio, video, archives), pass through
// untouched. Zero config.
type Gzip struct{}

func NewGzip() *Gzip { return &Gzip{} }

func (m *Gzip) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
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

type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
	skip        bool
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.wroteHeader {
		return
	}
	g.wroteHeader = true
	h := g.Header()
	if h.Get("Content-Encoding") != "" || incompressible(h.Get("Content-Type")) {
		g.skip = true
		g.ResponseWriter.WriteHeader(code)
		return
	}
	h.Set("Content-Encoding", "gzip")
	// The plain length no longer applies to the compressed stream.
	h.Del("Content-Length")
	g.gz = gzip.NewWriter(g.ResponseWriter)
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.skip {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		_ = g.gz.Close()
	}
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
