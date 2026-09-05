package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// AccessLog emits one slog line per request: method, path, status, body
// bytes, duration, remote. Zero config — it logs through slog.Default() at
// call time, so it follows whatever logger main installs.
type AccessLog struct{}

func NewAccessLog() *AccessLog { return &AccessLog{} }

func (m *AccessLog) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start),
			"remote", r.RemoteAddr,
		)
	})
}

// statusRecorder captures what the handler wrote. Unwrap keeps
// http.ResponseController features (flush, deadlines) reachable through it.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
