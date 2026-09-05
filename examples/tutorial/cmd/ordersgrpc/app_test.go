package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This exercises the fully wired app — real transport, real service
// layer, real auth — with none of the four infrastructure dependencies
// running, via NewTestApp's mocks. Like cmd/orders' equivalent it never
// calls app.Run: notifier opens its own NATS connection directly rather
// than through broker.EventPublisher, so it is not one of the interfaces
// Override replaced. Driving the handler directly sidesteps that.
func TestGraphWiresAndServesTheContract(t *testing.T) {
	// A config is required only while something in the graph consumes it:
	// the overrides replaced the postgres and redis consumers with mocks,
	// so their variables aren't needed here at all. NATS_URL still is —
	// the notifier takes natsbroker.Config and isn't overridden — and
	// JWT_SECRET matters for real, since auth.Issuer is unmocked.
	t.Setenv("NATS_URL", "unused-in-this-test")
	t.Setenv("JWT_SECRET", "test-secret")

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(func() {
		if r := app.Shutdown(context.Background()); !r.Clean() {
			t.Errorf("shutdown was not clean: %v", r)
		}
	})

	ts := httptest.NewServer(app.server.Handler())
	t.Cleanup(ts.Close)

	// The contract is public...
	resp, err := http.Get(ts.URL + "/openapi.yaml")
	if err != nil {
		t.Fatalf("GET /openapi.yaml: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /openapi.yaml = %d, want 200", resp.StatusCode)
	}

	// ...and the operational endpoints are not. They are served by
	// package admin on a separate listener, which main binds to
	// HTTP_ADMIN_ADDR and no ingress points at.
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d on the public listener, want 404", path, resp.StatusCode)
		}
	}
}
