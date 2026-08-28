// Package config loads and validates the service's runtime configuration
// from environment variables. It has no lifecycle capabilities of its own —
// New either returns a complete, valid Config or an error before anything
// else in the graph is constructed, since every other component depends on
// it directly or transitively.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8080"`

	// AdminAddr serves /healthz and /readyz (chapter 10), and /metrics
	// (chapter 13) — deliberately a separate listener from HTTPAddr; see
	// docs/tutorial/10-api-layer.md for why.
	AdminAddr string `env:"ADMIN_ADDR" envDefault:":8081"`

	PostgresDSN string `env:"POSTGRES_DSN,required"`
	RedisAddr   string `env:"REDIS_ADDR,required"`
	NATSURL     string `env:"NATS_URL,required"`

	// JWTSecret has no default on purpose — see docs/tutorial/03-configuration.md's
	// do's and don'ts. A missing secret must fail startup, never silently
	// fall back to something guessable.
	JWTSecret string        `env:"JWT_SECRET,required"`
	JWTExpiry time.Duration `env:"JWT_EXPIRY" envDefault:"1h"`

	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`

	RateLimitRPS float64 `env:"RATE_LIMIT_RPS" envDefault:"50"`

	// SessionRecent caps the per-user recently-viewed list. The linger
	// window and instance cap for that scope are *not* here: both are
	// baked into the generated code from servo.Scoped's arguments, which
	// the spec file declares as constants — see
	// docs/tutorial/12-scoped-instances.md.
	SessionRecent int `env:"SESSION_RECENT" envDefault:"10"`
}

func New() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
