package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/api"
	"example.com/servoorders/auth"
	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
	"example.com/servoorders/mocks"
	"example.com/servoorders/observability"
	"example.com/servoorders/resilience"
	"example.com/servoorders/service"
	"example.com/servoorders/session"
	"github.com/okian/servo/v3/servo"
	"go.uber.org/mock/gomock"
)

// fakeSessions is a hand-written stand-in for the accessor `servo generate`
// emits into package main. It is the payoff of api.Server depending on
// session.Sessions rather than on *session.Session: this package can be
// tested with no servo, no generated code, and no reference counting — and
// still gets one session per user rather than one shared by everybody,
// because it keys itself off the same ScopeKey method the real accessor
// calls.
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

func (f *fakeSessions) Stats() servo.ScopeStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return servo.ScopeStats{Live: len(f.by)}
}

// newTestServer wires a real api.Server on top of real service.OrderService
// and service.AuthService — but those, in turn, run on gomock mocks instead
// of postgres/redis/natsbroker. servo is not involved at all here: this is
// plain Go construction, the same three lines main.go's generated New would
// otherwise write for us. That's deliberate — servo.Override and a real
// NewTestApp don't show up until chapter 13; this chapter proves the HTTP
// contract on its own first.
func newTestServer(t *testing.T) (*httptest.Server, *mocks.MockOrderRepository, *auth.Issuer) {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	users := mocks.NewMockUserRepository(ctrl)
	orderCache := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	// OrderService always tries the cache — a miss on read and a
	// best-effort write on create, regardless of which test is running, so
	// these two are set up once here rather than repeated in every test.
	orderCache.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, cache.ErrMiss).AnyTimes()
	orderCache.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	// Each component gets its own narrow config, built as a literal. RPS
	// is set explicitly (rather than relying on the envDefault) because a
	// bare struct literal skips caarlos0/env's tag processing entirely —
	// every field not set here is the Go zero value, not the configured
	// default. A zero RPS would mean the rate limiter allows exactly one
	// request per test, ever; see
	// TestRateLimiterRejectsRequestsOverTheLimitAndCountsIt for the test that
	// actually wants that.
	authCfg := &auth.Config{Secret: "test-secret", Expiry: time.Hour}
	limitCfg := &resilience.Config{RPS: 1000}
	sessionCfg := &session.Config{Recent: 10}
	issuer := auth.New(authCfg)
	orders := service.New(repo, orderCache, pub, quietLogger())
	authSvc := service.NewAuthService(users, issuer)
	metrics := observability.NewMetrics()
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}

	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	testUser := &domain.User{ID: uuid.New(), Username: "alice", PasswordHash: hash}
	users.EXPECT().GetByUsername(gomock.Any(), "alice").Return(testUser, nil).AnyTimes()
	users.EXPECT().GetByUsername(gomock.Any(), "nobody").Return(nil, domain.ErrNotFound).AnyTimes()

	srv := api.New(&api.Config{}, orders, authSvc, issuer, metrics, tracer, resilience.NewRateLimiter(limitCfg, metrics), newFakeSessions(sessionCfg), quietLogger())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, repo, issuer
}

func loginAs(t *testing.T, ts *httptest.Server, username string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": "password123"})
	resp, err := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

func TestLoginSucceedsWithCorrectPassword(t *testing.T) {
	ts, _, _ := newTestServer(t)
	token := loginAs(t, ts, "alice")
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
}

func TestLoginFailsWithWrongUsername(t *testing.T) {
	ts, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]string{"username": "nobody", "password": "password123"})
	resp, err := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCreateOrderRequiresAuth(t *testing.T) {
	ts, _, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]any{"item": "widget", "quantity": 1})
	resp, err := http.Post(ts.URL+"/orders", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCreateOrderSucceedsWithValidToken(t *testing.T) {
	ts, repo, _ := newTestServer(t)
	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	token := loginAs(t, ts, "alice")
	body, _ := json.Marshal(map[string]any{"item": "widget", "quantity": 2})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/orders", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var out struct {
		Item     string `json:"item"`
		Quantity int    `json:"quantity"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Item != "widget" || out.Quantity != 2 {
		t.Errorf("got %+v, want item=widget quantity=2", out)
	}
}

func TestGetOrderReturns404ForUnknownID(t *testing.T) {
	ts, repo, _ := newTestServer(t)
	repo.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, domain.ErrNotFound)

	token := loginAs(t, ts, "alice")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/orders/"+uuid.New().String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestGetOrderReturns403ForAnotherUsersOrder(t *testing.T) {
	ts, repo, _ := newTestServer(t)
	otherUsersOrder := &domain.Order{ID: uuid.New(), UserID: uuid.New(), Item: "widget"}
	repo.EXPECT().Get(gomock.Any(), otherUsersOrder.ID).Return(otherUsersOrder, nil)

	token := loginAs(t, ts, "alice")
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/orders/"+otherUsersOrder.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

// This pins down a real bug from this chapter's own history: Run originally
// just called ListenAndServe and returned, with no select on ctx.Done() at
// all. servo's generated App.Run waits for every Runner before ever calling
// Shutdown, so that version hung forever on a real cancellation — nothing
// after Run() in main() ever ran, and the process never exited on SIGTERM.
// This test catches exactly that regression: it doesn't call Stop at all,
// so if Run ever again relies on Stop to make it return, it will time out.
func TestRunReturnsPromptlyWhenContextIsCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	apiCfg := &api.Config{HTTPAddr: "127.0.0.1:0"}
	limitCfg := &resilience.Config{RPS: 1000}
	issuer := auth.New(&auth.Config{Secret: "test-secret", Expiry: time.Hour})
	orders := service.New(mocks.NewMockOrderRepository(ctrl), mocks.NewMockOrderCache(ctrl), mocks.NewMockEventPublisher(ctrl), quietLogger())
	authSvc := service.NewAuthService(mocks.NewMockUserRepository(ctrl), issuer)
	tracer, err := observability.NewTracer(&observability.Config{})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	testMetrics := observability.NewMetrics()
	srv := api.New(apiCfg, orders, authSvc, issuer, testMetrics, tracer, resilience.NewRateLimiter(limitCfg, testMetrics), newFakeSessions(&session.Config{Recent: 10}), quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation")
	}
}

// TestRecentRemembersWhatThisUserViewed is the end-to-end shape of a
// scope: two requests from the same person, and the second one sees state
// the first one left behind — without a database, and without that state
// being visible to anybody else.
func TestRecentRemembersWhatThisUserViewed(t *testing.T) {
	ts, repo, issuer := newTestServer(t)

	token := loginAs(t, ts, "alice")
	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	order := &domain.Order{ID: uuid.New(), UserID: claims.UserID, Item: "widget", Quantity: 1, Status: domain.OrderStatusPending}
	repo.EXPECT().Get(gomock.Any(), order.ID).Return(order, nil).AnyTimes()

	if code := authedGet(t, ts, token, "/orders/"+order.ID.String()); code != http.StatusOK {
		t.Fatalf("GET order status = %d, want 200", code)
	}

	var mine struct {
		Recent []uuid.UUID `json:"recent"`
	}
	if code := authedGetJSON(t, ts, token, "/me/recent", &mine); code != http.StatusOK {
		t.Fatalf("GET /me/recent status = %d, want 200", code)
	}
	if len(mine.Recent) != 1 || mine.Recent[0] != order.ID {
		t.Fatalf("recent = %v, want just the order that was viewed", mine.Recent)
	}
}

// TestRecentRejectsAnUnauthenticatedCaller is the other half of the
// contract. Without requireAuth there is no key in the context, so
// ScopeKey returns servo.ErrNoScopeKey rather than the zero UserID — which
// is exactly what stops every anonymous caller from sharing one session.
func TestRecentRejectsAnUnauthenticatedCaller(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/me/recent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestRecentIsEmptyForANewSession covers the cold-key path through the
// accessor: a user who has viewed nothing gets an empty list rather than
// an error or somebody else's.
func TestRecentIsEmptyForANewSession(t *testing.T) {
	ts, _, _ := newTestServer(t)
	token := loginAs(t, ts, "alice")

	var out struct {
		Recent []uuid.UUID `json:"recent"`
	}
	if code := authedGetJSON(t, ts, token, "/me/recent", &out); code != http.StatusOK {
		t.Fatalf("GET /me/recent status = %d, want 200", code)
	}
	if len(out.Recent) != 0 {
		t.Fatalf("recent = %v, want empty", out.Recent)
	}
}

func authedGet(t *testing.T, ts *httptest.Server, token, path string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func authedGetJSON(t *testing.T, ts *httptest.Server, token, path string, into any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		json.NewDecoder(resp.Body).Decode(into)
	}
	return resp.StatusCode
}

// quietLogger is the owned logger type with its output discarded, so a
// test exercises the same code path production does without writing to
// stdout.
func quietLogger() *observability.Logger {
	return &observability.Logger{Logger: slog.New(slog.DiscardHandler)}
}

// TestAdminEndpointsAreNotOnThePublicListener is the one boundary in this
// service enforced by a test rather than by convention. /healthz and
// /readyz name every component in the graph along with its status, and
// /metrics carries request rates and error counts per route — together
// they describe the system precisely enough to be worth hiding, so they
// live on the admin listener and nowhere else (see package admin).
//
// Adding any of them to this router — the kind of change that looks
// harmless in review — fails here. The ginapi and grpcapi variants carry
// the same assertion.
func TestAdminEndpointsAreNotOnThePublicListener(t *testing.T) {
	ts, _, _ := newTestServer(t)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get(ts.URL + path)
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
