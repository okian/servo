package servoapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/config"
	"example.com/servoorders/internal/session"
)

type contextKey int

const claimsKey contextKey = 0

// Auth is requireAuth, one transport later: the same Bearer parsing, the
// same auth.Verify, the same two context plantings — the claims for the
// handlers (via ClaimsExtractor) and the session key for servo's generated
// accessor. The difference is the attachment: instead of wrapping each
// route by hand in server.go, the spec selects the protected routes with
// servo.Use[*servoapi.Auth](servo.Route(...), ...).
type Auth struct {
	issuer *auth.Issuer
}

func NewAuth(issuer *auth.Issuer) *Auth { return &Auth{issuer: issuer} }

func (m *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || token == "" {
			unauthorized(w, "missing or malformed Authorization header")
			return
		}
		claims, err := m.issuer.Verify(token)
		if err != nil {
			unauthorized(w, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), claimsKey, claims)
		ctx = session.WithUser(ctx, session.UserID(claims.UserID.String()))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// unauthorized mirrors the generated adapters' error shape, so a client
// sees one body whether the middleware or a handler refused it.
func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: msg})
}

// ClaimsExtractor turns every handler parameter of type auth.Claims into a
// per-request value: it reads what Auth planted, so the token is verified
// exactly once. The 401 here is the belt to Auth's suspenders — it only
// fires if a claims-taking route was left out of Auth's selector list.
type ClaimsExtractor struct{}

func NewClaimsExtractor() *ClaimsExtractor { return &ClaimsExtractor{} }

func (e *ClaimsExtractor) Extract(r *http.Request) (auth.Claims, error) {
	claims, ok := r.Context().Value(claimsKey).(auth.Claims)
	if !ok {
		return auth.Claims{}, servo.Status.UNAUTHORIZED.New("missing bearer token")
	}
	return claims, nil
}

// listenConfig reads the same HTTP_ADDR every deployment already sets for
// this service — the api transport's Config does too, but each injector
// resolves only its own.
type listenConfig struct {
	Addr string `env:"HTTP_ADDR" envDefault:":8080"`
}

// NewHTTPConfig is the node servo.HTTP() requires: the user constructs it,
// so where the values come from stays this module's business.
func NewHTTPConfig(src config.Source) (*servo.HTTPConfig, error) {
	lc, err := config.Parse[listenConfig](src, "")
	if err != nil {
		return nil, err
	}
	host, portStr, err := net.SplitHostPort(lc.Addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}
	return &servo.HTTPConfig{IP: host, Port: uint16(port)}, nil
}
