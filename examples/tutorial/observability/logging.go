// Package observability holds every cross-cutting concern that isn't
// business logic: structured logging, Prometheus metrics, and OpenTelemetry
// tracing.
package observability

import (
	"log/slog"
	"os"

	"example.com/servoorders/config"
)

// Config carries the two settings this package reads. It takes no prefix:
// LOG_LEVEL and OTLP_ENDPOINT are app-wide by convention and are spelled
// that way in every deployment already.
type Config struct {
	LogLevel     string `env:"LOG_LEVEL" envDefault:"info"`
	OTLPEndpoint string `env:"OTLP_ENDPOINT" envDefault:""`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, "")
}

// NewLogger is an ordinary provider, which is the whole point: anything
// that logs takes a *slog.Logger, so the logger is constructed before it
// by the same rule that orders everything else. There is no "did logging
// get set up first" question to answer, because a component that logs
// cannot be built without it.
//
// It also sets the process-wide default. That is for code servo does not
// wire — the standard library and third-party packages log through
// slog.Default() and have no constructor to inject into. Our own code
// never relies on it: every component here takes the logger.
func NewLogger(cfg *Config) *Logger {
	l := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(cfg.LogLevel),
	}))
	slog.SetDefault(l)
	return &Logger{Logger: l}
}

// Logger is a defined type rather than a bare *slog.Logger because a
// foreign type comes with foreign providers. The standard library's
// slog.Default and a transitive dependency's logr helper both return
// *slog.Logger, and servo has no basis for choosing between them — it
// reports the ambiguity rather than guessing. Owning the type is the same
// rule as owning your configuration: depend on what you declare.
//
// The embedded *slog.Logger means callers write log.InfoContext(...)
// exactly as they would have.
type Logger struct{ *slog.Logger }

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
