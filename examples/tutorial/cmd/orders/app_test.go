package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"uuid"

	"example.com/servoorders/auth"
	"example.com/servoorders/domain"
	"go.uber.org/mock/gomock"
)

// This exercises the fully wired app — real api.Server, real
// service.OrderService/AuthService, real auth.Issuer — with none of the
// four infrastructure dependencies actually running, via NewTestApp's
// mocks. It deliberately never calls app.Run: notifier isn't behind
// broker.EventPublisher (it opens its own NATS connection directly, see
// docs/tutorial/07-messaging-layer.md), so it isn't one of the four
// interfaces Override replaced, and Run would still try to reach real
// NATS. Testing the HTTP surface directly through app.server.Handler()
// sidesteps that entirely — see docs/tutorial/13-wiring-with-servo.md.
func TestFullAPIFlowWithMockedInfrastructure(t *testing.T) {
	// Config itself isn't one of the four overridden interfaces — it's a
	// concrete type nothing stands in for — so every required field still
	// has to be set even though the mocked path never actually dials
	// Postgres, Redis, or NATS with these values.
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
	app.orderCacheForServo.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil)
	app.orderRepositoryForServo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	app.eventPublisherForServo.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil)

	ts := httptest.NewServer(app.server.Handler())
	defer ts.Close()

	loginBody, _ := json.Marshal(map[string]string{"username": "alice", "password": "password123"})
	loginResp, err := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", loginResp.StatusCode)
	}
	var loginOut struct {
		Token string `json:"token"`
	}
	json.NewDecoder(loginResp.Body).Decode(&loginOut)

	createBody, _ := json.Marshal(map[string]any{"item": "widget", "quantity": 1})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/orders", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+loginOut.Token)
	createResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create order status = %d, want 201", createResp.StatusCode)
	}

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Errorf("Shutdown not clean: %v", r)
	}
}
