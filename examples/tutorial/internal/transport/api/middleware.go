package api

import (
	"context"
	"net/http"
	"strings"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/session"
)

type contextKey int

const claimsKey contextKey = 0

// requireAuth extracts and verifies the Bearer token, then injects the
// resulting auth.Claims into the request context — auth.Verify does the
// actual JWT work; this is only the HTTP-specific plumbing around it
// (header parsing, the 401 response shape). Keeping that split is what
// lets auth stay usable from a transport other than HTTP without changes.
func requireAuth(issuer *auth.Issuer, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		claims, err := issuer.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		// The claims are what the handlers read; the session key is what
		// servo's generated accessor reads. Both go in here, at the one
		// point in the request where the user's identity is first known —
		// see docs/tutorial/14-scoped-instances.md.
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
		next(w, r.WithContext(ctx))
	}
}

func claimsFromContext(ctx context.Context) (auth.Claims, bool) {
	claims, ok := ctx.Value(claimsKey).(auth.Claims)
	return claims, ok
}

// recoverMiddleware turns a panic anywhere in the handler chain into a 500
// instead of taking down the whole process — one bad request must never be
// able to crash every other in-flight request alongside it.
func recoverMiddleware(log *observability.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(r.Context(), "api: panic recovered", "panic", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// loggingMiddleware is deliberately minimal for now — method, path, status,
// duration. Chapter 15 replaces the ad hoc status-capturing here with a
// proper response wrapper shared with metrics, and correlates each line
// with the request's trace ID.
func loggingMiddleware(log *observability.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		log.InfoContext(r.Context(), "request",
			"method", r.Method, "path", r.URL.Path, "status", sw.status)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
