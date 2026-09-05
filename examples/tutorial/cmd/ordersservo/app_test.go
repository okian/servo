package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/domain"
	"go.uber.org/mock/gomock"
)

// The same fully wired flow cmd/orders tests through app.server.Handler(),
// one transport later: NewTestApp swaps the four infrastructure interfaces
// for mocks, and app.HTTPHandler("") is the generated server's routed,
// middleware-wrapped handler — no listener bound, no containers running.
// The whole chain runs for real: RateLimiter, Tracer, Metrics, the Auth
// middleware planting claims and the session key, the ClaimsExtractor, the
// generated decoding, and the handlers.
func TestFullAPIFlowThroughGeneratedTransport(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "unused-in-this-test")
	t.Setenv("REDIS_ADDR", "unused-in-this-test")
	t.Setenv("NATS_URL", "unused-in-this-test")
	t.Setenv("JWT_SECRET", "test-secret")

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() {
		app.userRepositoryForServo.Finish()
		app.orderRepositoryForServo.Finish()
		app.orderCacheForServo.Finish()
		app.eventPublisherForServo.Finish()
	})

	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	testUser := &domain.User{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Username: "alice", PasswordHash: hash}
	app.userRepositoryForServo.EXPECT().GetByUsername(gomock.Any(), "alice").Return(testUser, nil)
	app.orderRepositoryForServo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	app.orderCacheForServo.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil)
	app.eventPublisherForServo.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil)

	h := app.HTTPHandler("")
	if h == nil {
		t.Fatalf("no default-group handler")
	}

	// Login — the one route outside Auth's selector list.
	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "password123"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", w.Code, w.Body.String())
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil || login.Token == "" {
		t.Fatalf("login body: %v %s", err, w.Body.String())
	}

	// Without the token, the Auth middleware refuses before anything runs.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader([]byte(`{"item":"espresso","quantity":2}`))))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create = %d, want 401", w.Code)
	}

	// With it, the generated adapter decodes the body, the extractor hands
	// the handler its claims, and the status in the error position picks 201.
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader([]byte(`{"item":"espresso","quantity":2}`)))
	req.Header.Set("Authorization", "Bearer "+login.Token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Item     string `json:"item"`
		Quantity int    `json:"quantity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Item != "espresso" || created.Quantity != 2 {
		t.Fatalf("create body: %v %s", err, w.Body.String())
	}

	// The scoped session answers /me/recent without touching any mock:
	// the Auth middleware planted the key, the accessor acquired the
	// per-user instance, and nothing has been viewed yet. The request gets
	// a cancellable context because Acquire refuses one without a Done
	// channel — a real server's request contexts always have one;
	// httptest.NewRequest's does not.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req = httptest.NewRequest(http.MethodGet, "/me/recent", nil).WithContext(reqCtx)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("recent = %d: %s", w.Code, w.Body.String())
	}
	var recent struct {
		Recent []uuid.UUID `json:"recent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &recent); err != nil || len(recent.Recent) != 0 {
		t.Fatalf("recent body: %v %s", err, w.Body.String())
	}
}
