package servoapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/auth"
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

// ListenConfig reads HTTP_ADDR the same way the api transport's Config
// does — a //servo:config struct whose loader the generator emits, so this
// injector never writes env-parsing code. Each injector resolves only its
// own config, so sharing the HTTP_ADDR spelling is not a collision.
//
// HTTP_SHUTDOWN_DELAY is how long the listener goes on serving once
// Shutdown has marked it not ready, so a load balancer — or Kubernetes,
// taking the pod out of its endpoints — stops routing here before the
// socket closes. Unset, it is zero, which suits docker-compose and a
// laptop; behind a load balancer, a few seconds, with the orchestrator's
// grace period above the delay plus the slowest request.
//
//servo:config prefix=HTTP
type ListenConfig struct {
	Addr          string        `config:"addr,default=:8080"`
	ShutdownDelay time.Duration `config:"shutdown_delay"`
}

// NewHTTPConfig is the node servo.HTTP() requires: it turns the loaded
// address into the servo.HTTPConfig the emitted server binds. The listen
// config arrives by value, built by the generated loader.
//
// The timeouts are this service's facts rather than settings: zero would
// mean none, as it does on net/http.Server, and a server with no header
// timeout lets a client that never finishes its headers hold a connection
// open for good — the generated server warns at startup when nothing
// bounds the header read. The bodies here are small JSON, so a whole
// request and a whole response each fit comfortably in fifteen seconds.
func NewHTTPConfig(cfg ListenConfig) (*servo.HTTPConfig, error) {
	host, portStr, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}
	return &servo.HTTPConfig{
		IP:                host,
		Port:              uint16(port),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       time.Minute,
		ShutdownDelay:     cfg.ShutdownDelay,
	}, nil
}
