package observability

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type Tracer struct {
	provider *sdktrace.TracerProvider
}

// NewTracer sets the provider up completely, synchronously — not in a
// separate Init. api.Server's own constructor needs a working *Tracer
// immediately, to wrap its handler; servo only guarantees a dependency's
// constructor has already returned by the time a dependent's constructor
// runs, not that its Init has. Splitting this into New (build) + Init
// (start) the way postgres.Store does would hand api.Server a *Tracer
// with a nil provider. Spans are still created when OTLPEndpoint is
// unset, just never exported — tracing is opt-in, not a required
// dependency this service can't start without. See
// docs/tutorial/13-observability.md.
func NewTracer(cfg *Config) (*Tracer, error) {
	ctx := context.Background()
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName("servoorders"),
	))
	if err != nil {
		return nil, fmt.Errorf("observability: resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if cfg.OTLPEndpoint != "" {
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("observability: otlp exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	return &Tracer{provider: sdktrace.NewTracerProvider(opts...)}, nil
}

func (t *Tracer) Stop(ctx context.Context) error {
	return t.provider.Shutdown(ctx)
}

// Middleware wraps next with a span per request, named after the matched
// route (see Metrics.Middleware for why the pattern, not the raw path).
func (t *Tracer) Middleware(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "http.request",
		otelhttp.WithTracerProvider(t.provider),
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return operation
		}),
	)
}
