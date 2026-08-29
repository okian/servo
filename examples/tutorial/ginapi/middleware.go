package ginapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"example.com/servoorders/auth"
	"example.com/servoorders/domain"
	"example.com/servoorders/observability"
	"example.com/servoorders/session"
)

// claimsKey is a plain string here, not the unexported context-key type
// the net/http variant uses: Gin's Set/Get are its own per-request store,
// keyed by string, and never touch context.Context's key space. The
// collision that motivates a private key type cannot arise.
const claimsKey = "claims"

// requireAuth is Gin middleware rather than a handler wrapper. That is
// the shape difference worth noticing between the two variants: net/http
// composes `requireAuth(issuer, handler)` per route, while Gin registers
// the middleware once on a route group and every route in the group
// inherits it.
func requireAuth(issuer *auth.Issuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			abort(c, http.StatusUnauthorized, "missing or malformed Authorization header")
			return
		}

		claims, err := issuer.Verify(token)
		if err != nil {
			abort(c, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		// The claims are what handlers read; the session key is what
		// servo's generated accessor reads. Both are set at the one point
		// in the request where the user's identity is first known — see
		// docs/tutorial/14-scoped-instances.md.
		c.Set(claimsKey, claims)
		c.Request = c.Request.WithContext(
			session.WithUser(c.Request.Context(), session.UserID(claims.UserID.String())),
		)
		c.Next()
	}
}

func claimsFrom(c *gin.Context) auth.Claims {
	// requireAuth guarantees this is present: every route that calls it is
	// registered inside the authenticated group.
	claims, _ := c.MustGet(claimsKey).(auth.Claims)
	return claims
}

// loggingMiddleware replaces gin.Logger(), which writes its own text
// format to stdout. This service logs structured JSON through the
// injected logger, and a second format interleaved with it would make
// both harder to consume.
func loggingMiddleware(log *observability.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.InfoContext(c.Request.Context(), "request",
			"method", c.Request.Method,
			"path", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}

// recoverMiddleware replaces gin.Recovery() for the same reason.
func recoverMiddleware(log *observability.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(c.Request.Context(), "ginapi: panic recovered",
					"panic", rec, "path", c.Request.URL.Path)
				abort(c, http.StatusInternalServerError, "internal server error")
			}
		}()
		c.Next()
	}
}

// abort writes the error body and stops the chain. Gin needs both: writing
// a response does not by itself prevent later handlers from running.
func abort(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, errorResponse{Error: message})
}

// abortDomainError is the one place a domain sentinel becomes a status
// code, exactly as in the net/http variant — the mapping belongs to the
// transport, and nothing below this layer knows what an HTTP status is.
func abortDomainError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		abort(c, http.StatusNotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		abort(c, http.StatusForbidden, "forbidden")
	case errors.Is(err, domain.ErrInvalidCredentials):
		abort(c, http.StatusUnauthorized, "invalid credentials")
	case errors.Is(err, domain.ErrValidation):
		abort(c, http.StatusBadRequest, err.Error())
	default:
		abort(c, http.StatusInternalServerError, "internal server error")
	}
}
