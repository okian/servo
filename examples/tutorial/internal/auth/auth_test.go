package auth_test

import (
	"strings"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/internal/auth"
)

func TestIssueThenVerifyRoundTrips(t *testing.T) {
	issuer, err := auth.New(auth.Config{Secret: "test-secret-of-at-least-32-bytes", Expiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
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
	issuer, err := auth.New(auth.Config{Secret: "test-secret-of-at-least-32-bytes", Expiry: -time.Hour}) // already expired
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Issue(uuid.New(), "alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := issuer.Verify(token); err == nil {
		t.Fatal("expected Verify to reject an expired token")
	}
}

func TestVerifyRejectsATokenSignedWithADifferentSecret(t *testing.T) {
	issuerA, err := auth.New(auth.Config{Secret: "secret-a-padded-to-32-bytes-long", Expiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	issuerB, err := auth.New(auth.Config{Secret: "secret-b-padded-to-32-bytes-long", Expiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

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

// TestNewRefusesAShortSecret pins the constructor's invariant: `required` on
// the config tag accepts an empty JWT_SECRET, so New is where it is refused.
func TestNewRefusesAShortSecret(t *testing.T) {
	for _, secret := range []string{"", "short", "thirty-one-bytes-is-one-too-few"} {
		if _, err := auth.New(auth.Config{Secret: secret, Expiry: time.Hour}); err == nil {
			t.Errorf("New accepted a %d-byte secret", len(secret))
		} else if secret != "" && strings.Contains(err.Error(), secret) {
			t.Errorf("New's error echoes the secret: %v", err)
		}
	}
	if _, err := auth.New(auth.Config{Secret: "exactly-thirty-two-bytes-long-ok", Expiry: time.Hour}); err != nil {
		t.Errorf("New refused a 32-byte secret: %v", err)
	}
}
