package ginapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
	"go.uber.org/mock/gomock"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/mocks"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/resilience"
	"example.com/servoorders/internal/service"
	"example.com/servoorders/internal/session"
	"example.com/servoorders/internal/transport/ginapi"
)

// The suite below drives the Gin router the same way api/api_test.go
// drives the net/http one, through a real httptest server. The assertions
// are deliberately about behaviour a reader comparing the two variants
// would want identical: status codes, error bodies, and which routes
// require a token.

func TestSwaggerAndSpecAreServedOnThePublicListener(t *testing.T) {
	srv := httptest.NewServer(newTestServer(t).Handler())
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ path, wantSubstring string }{
		{"/openapi.yaml", "openapi:"},
		{"/swagger/", "SwaggerUIBundle"},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		body, _ := readAllClose(resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, resp.StatusCode)
		}
		if !strings.Contains(body, tc.wantSubstring) {
			t.Errorf("GET %s body missing %q:\n%s", tc.path, tc.wantSubstring, body)
		}
	}
}

// The admin endpoints must never appear on the public listener. This is
// the assertion that keeps the security boundary from eroding by
// accident: adding /metrics to the router would fail here.
func TestAdminEndpointsAreNotOnThePublicListener(t *testing.T) {
	srv := httptest.NewServer(newTestServer(t).Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d on the public listener, want 404 — admin endpoints belong on the admin port only",
				path, resp.StatusCode)
		}
	}
}

func TestProtectedRoutesRejectAMissingToken(t *testing.T) {
	srv := httptest.NewServer(newTestServer(t).Handler())
	t.Cleanup(srv.Close)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/orders"},
		{http.MethodGet, "/orders"},
		{http.MethodGet, "/me/recent"},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		body, _ := readAllClose(resp)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Errorf("%s %s body is not the JSON error shape: %s", tc.method, tc.path, body)
		} else if got["error"] == "" {
			t.Errorf("%s %s body has no error message: %s", tc.method, tc.path, body)
		}
	}
}

func readAllClose(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String(), nil
		}
	}
}

func quietLogger() *observability.Logger {
	return &observability.Logger{Logger: slog.New(slog.DiscardHandler)}
}

// fakeSessions is the same stand-in api_test.go uses: the accessor
// interface is two methods, so a test can implement it directly and skip
// servo, generated code and reference counting entirely — while still
// getting one session per user, because it keys itself off the same
// ScopeKey method the real accessor calls.
type fakeSessions struct {
	cfg *session.Config
	mu  sync.Mutex
	by  map[session.UserID]*session.Session
}

func newFakeSessions(cfg *session.Config) *fakeSessions {
	return &fakeSessions{cfg: cfg, by: map[session.UserID]*session.Session{}}
}

func (f *fakeSessions) Acquire(ctx context.Context) (*session.Session, func(), error) {
	var zero *session.Session
	key, err := zero.ScopeKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.by[key]
	if !ok {
		s = session.New(key, f.cfg, quietLogger())
		f.by[key] = s
	}
	return s, func() {}, nil
}

func (f *fakeSessions) Stats() servo.ScopeStats { return servo.ScopeStats{} }

// newTestServer builds a Gin server over mocked infrastructure. Nothing
// here is Gin-specific except the constructor call itself, which is the
// point being demonstrated.
func newTestServer(t *testing.T) *ginapi.Server {
	t.Helper()
	ctrl := gomock.NewController(t)

	repo := mocks.NewMockOrderRepository(ctrl)
	users := mocks.NewMockUserRepository(ctrl)
	orderCache := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	orderCache.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, cache.ErrMiss).AnyTimes()
	orderCache.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	users.EXPECT().GetByUsername(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound).AnyTimes()

	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})
	metrics := observability.NewMetrics()
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	return ginapi.New(
		&ginapi.Config{},
		service.New(repo, orderCache, pub, quietLogger()),
		service.NewAuthService(users, issuer),
		issuer,
		metrics,
		tracer,
		resilience.NewRateLimiter(&resilience.Config{RPS: 1000}, metrics),
		newFakeSessions(&session.Config{Recent: 10}),
		quietLogger(),
	)
}
