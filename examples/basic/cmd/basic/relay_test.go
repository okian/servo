package main

import (
	"context"
	"strings"
	"testing"
)

// TestRelayUsesTwoDistinctAccounts proves the two queue.Client instances
// really are independent: different account IDs, both reachable from one
// component, with no ambiguity at generation time (see relay.Relay and
// queue.OrdersAccount/AuditAccount).
func TestRelayUsesTwoDistinctAccounts(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	if !strings.Contains(app.relay.OrdersResult, "111111111111") {
		t.Errorf("OrdersResult = %q, want it to reference the orders account", app.relay.OrdersResult)
	}
	if !strings.Contains(app.relay.AuditResult, "222222222222") {
		t.Errorf("AuditResult = %q, want it to reference the audit account", app.relay.AuditResult)
	}
	if app.relay.OrdersResult == app.relay.AuditResult {
		t.Error("expected two distinct account results, got identical ones")
	}
}
