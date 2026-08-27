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
}

func New() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}
