package graph

// OtelTracePackagePath is the OpenTelemetry trace API package a
// servo.Trace wrapper is built against: its TracerProvider is the graph
// node every wrapper depends on, and its Tracer and Span are what the
// generated methods call. OtelCodesPackagePath carries the status a failed
// span is marked with. Both are imported by generated code, in the user's
// module — servo's own module never depends on either, the same way the
// yaml and toml decoders a config file needs are the user's dependency.
const (
	OtelTracePackagePath = "go.opentelemetry.io/otel/trace"
	OtelCodesPackagePath = "go.opentelemetry.io/otel/codes"
	// OtelTracerProviderName is the interface the wrapper takes, looked up
	// in OtelTracePackagePath's scope.
	OtelTracerProviderName = "TracerProvider"
)
