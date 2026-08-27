package auth_test

import (
	"testing"
	"time"

	"example.com/servoorders/auth"
	"example.com/servoorders/config"
	"github.com/google/uuid"
)

func TestIssueThenVerifyRoundTrips(t *testing.T) {
	issuer := auth.New(&config.Config{JWTSecret: "test-secret", JWTExpiry: time.Hour})
	userID := uuid.New()

	token, err := issuer.Issue(userID, "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	claims, err := issuer.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != userID || claims.Username != "alice" {
		t.Errorf("Verify returned %+v, want UserID=%s Username=alice", claims, userID)
	}
}

func TestVerifyRejectsAnExpiredToken(t *testing.T) {
	issuer := auth.New(&config.Config{JWTSecret: "test-secret", JWTExpiry: -time.Hour}) // already expired
	token, err := issuer.Issue(uuid.New(), "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := issuer.Verify(token); err == nil {
		t.Fatal("expected Verify to reject an expired token")
	}
}

func TestVerifyRejectsATokenSignedWithADifferentSecret(t *testing.T) {
	issuerA := auth.New(&config.Config{JWTSecret: "secret-a", JWTExpiry: time.Hour})
	issuerB := auth.New(&config.Config{JWTSecret: "secret-b", JWTExpiry: time.Hour})

	token, err := issuerA.Issue(uuid.New(), "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := issuerB.Verify(token); err == nil {
		t.Fatal("expected Verify to reject a token signed with a different secret")
	}
}

func TestHashPasswordThenCheckPasswordRoundTrips(t *testing.T) {
	hash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := auth.CheckPassword(hash, "password123"); err != nil {
		t.Errorf("CheckPassword with the correct password: %v, want nil", err)
	}
	if err := auth.CheckPassword(hash, "wrong-password"); err == nil {
		t.Error("CheckPassword with the wrong password: got nil, want an error")
	}
}
