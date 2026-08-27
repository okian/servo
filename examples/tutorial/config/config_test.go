package config

import (
	"os"
	"testing"
	"time"
)

// unsetForTest unsets an env var for the duration of the test and restores
// whatever was there before — t.Setenv can only set a value, not remove
// one, and a required-field test needs the var to be genuinely absent, not
// merely present-but-empty (env.Parse's "required" check looks at whether
// the variable was set at all).
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	orig, wasSet := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(key, orig)
		}
	})
}

func TestNewFailsWhenARequiredVarIsMissing(t *testing.T) {
	unsetForTest(t, "POSTGRES_DSN")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("JWT_SECRET", "test-secret")

	if _, err := New(); err == nil {
		t.Fatal("expected an error when POSTGRES_DSN is unset")
	}
}

func TestNewAppliesDefaultsAndParsesTypedFields(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://localhost/orders")
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("JWT_EXPIRY", "30m")

	cfg, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default \":8080\"", cfg.HTTPAddr)
	}
	if cfg.JWTExpiry != 30*time.Minute {
		t.Errorf("JWTExpiry = %v, want 30m parsed from env", cfg.JWTExpiry)
	}
	if cfg.RateLimitRPS != 50 {
		t.Errorf("RateLimitRPS = %v, want default 50", cfg.RateLimitRPS)
	}
}
