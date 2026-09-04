package observability_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/servoorders/internal/observability"
	"go.opentelemetry.io/otel/trace"
)

func TestNewTracerWithNoEndpointStillCreatesSpans(t *testing.T) {
	tracer, err := observability.NewTracer(observability.Config{OTLPEndpoint: ""})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	t.Cleanup(func() { tracer.Stop(context.Background()) })

	var sawSpanContext bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		sawSpanContext = trace.SpanContextFromContext(r.Context()).IsValid()
	})

	handler := tracer.Middleware(mux)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orders/abc", nil))

	if !sawSpanContext {
		t.Error("expected a valid span context inside the handler even with OTLPEndpoint unset — spans are still created, just never exported")
	}
}

func TestNewTracerRejectsAnUnreachableEndpointOnlyAtExportTimeNotConstruction(t *testing.T) {
	// otlptracehttp.New doesn't dial anything at construction time — export
	// happens asynchronously, in the background, later. So NewTracer must
	// succeed here even though nothing is listening on this port; this is
	// worth pinning down since it's easy to assume the opposite.
	tracer, err := observability.NewTracer(observability.Config{OTLPEndpoint: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewTracer: %v, want it to succeed even with an unreachable endpoint", err)
	}
	tracer.Stop(context.Background())
}
