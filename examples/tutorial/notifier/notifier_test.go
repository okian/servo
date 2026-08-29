package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/servoorders/broker"
	"example.com/servoorders/observability"
	"github.com/nats-io/nats.go"
)

// TestRunLogsReceivedEvents starts Run in the background (it blocks on
// ctx.Done, like any Runner-capability component's Run), publishes one
// event on a separate connection, and asserts it was logged — then cancels
// and asserts Run actually returns instead of leaking the goroutine.
func TestRunLogsReceivedEvents(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set; see docs/tutorial/07-messaging-layer.md")
	}

	// The logger is injected, not global, so the buffer has to be injected
	// too. Swapping slog's package-level default would capture nothing:
	// the Notifier writes to whatever it was handed — see
	// docs/tutorial/13-observability.md for why it takes one at all.
	var logs bytes.Buffer
	capture := &observability.Logger{Logger: slog.New(slog.NewTextHandler(&logs, nil))}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	n := New(&Config{URL: url}, capture)
	go func() { done <- n.Run(ctx) }()

	pub, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect publisher: %v", err)
	}
	defer pub.Close()

	// Run's Subscribe call happens asynchronously after connecting; a short
	// retry loop is simpler and less flaky here than guessing a fixed delay.
	event, _ := json.Marshal(broker.OrderPlacedEvent{OrderID: "order-123", UserID: "user-456", Item: "widget"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := pub.Publish(broker.OrderPlacedSubject, event); err != nil {
			t.Fatalf("publish: %v", err)
		}
		pub.Flush()
		if strings.Contains(logs.String(), "order-123") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(logs.String(), "order-123") || !strings.Contains(logs.String(), "widget") {
		t.Fatalf("logs = %q, want a line mentioning order-123 and widget", logs.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil after context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of cancellation")
	}
}
