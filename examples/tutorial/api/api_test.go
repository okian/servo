package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/servoorders/api"
	"example.com/servoorders/auth"
	"example.com/servoorders/cache"
	"example.com/servoorders/config"
	"example.com/servoorders/domain"
	"example.com/servoorders/mocks"
	"example.com/servoorders/observability"
	"example.com/servoorders/resilience"
	"example.com/servoorders/service"
	"github.com/google/uuid"
	"go.uber.org/mock/gomock"
)

// newTestServer wires a real api.Server on top of real service.OrderService
// and service.AuthService — but those, in turn, run on gomock mocks instead
// of postgres/redis/natsbroker. servo is not involved at all here: this is
// plain Go construction, the same three lines main.go's generated New would
// otherwise write for us. That's deliberate — servo.Override and a real
// NewTestApp don't show up until chapter 11; this chapter proves the HTTP
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

	// RateLimitRPS is set explicitly (rather than relying on Config's own
	// envDefault) because a bare &config.Config{} literal skips
	// caarlos0/env's tag processing entirely — every field not set here
	// is the Go zero value, not the configured default. A zero
	// RateLimitRPS would mean the rate limiter allows exactly one request
	// per test, ever; see TestRateLimiterRejectsRequestsOverTheLimit for
	// the test that actually wants that.
	cfg := &config.Config{JWTSecret: "test-secret", JWTExpiry: time.Hour, RateLimitRPS: 1000}
	issuer := auth.New(cfg)
	orders := service.New(repo, orderCache, pub)
	authSvc := service.NewAuthService(users, issuer)
	metrics := observability.NewMetrics()
	tracer, err := observability.NewTracer(cfg)
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

	srv := api.New(cfg, orders, authSvc, issuer, metrics, tracer, resilience.NewRateLimiter(cfg, metrics))
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
	cfg := &config.Config{HTTPAddr: "127.0.0.1:0", JWTSecret: "test-secret", JWTExpiry: time.Hour, RateLimitRPS: 1000}
	issuer := auth.New(cfg)
	orders := service.New(mocks.NewMockOrderRepository(ctrl), mocks.NewMockOrderCache(ctrl), mocks.NewMockEventPublisher(ctrl))
	authSvc := service.NewAuthService(mocks.NewMockUserRepository(ctrl), issuer)
	tracer, err := observability.NewTracer(cfg)
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	testMetrics := observability.NewMetrics()
	srv := api.New(cfg, orders, authSvc, issuer, testMetrics, tracer, resilience.NewRateLimiter(cfg, testMetrics))

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
