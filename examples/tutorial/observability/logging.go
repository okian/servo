// Package observability holds every cross-cutting concern that isn't
// business logic: structured logging, Prometheus metrics, and OpenTelemetry
// tracing.
package observability

import (
	"log/slog"
	"os"

	"example.com/servoorders/config"
)

// ConfigureLogging is a plain function, not a servo-managed component —
// deliberately. Logging setup has to happen before anything else has a
// chance to log, and the only way to guarantee that is to call it directly
// at the top of main, before New(ctx) constructs anything at all. Making it
// a component (even a root) would only mean asking "did it run before
// everything else" instead of just knowing it did.
// Config carries the two settings this package reads. It takes no
// prefix: LOG_LEVEL and OTLP_ENDPOINT are app-wide by convention and are
// spelled that way in every deployment already.
type Config struct {
	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, "")
}

func ConfigureLogging(cfg *Config) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	})))
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
