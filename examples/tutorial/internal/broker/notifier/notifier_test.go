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

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/broker/natsbroker"
	"example.com/servoorders/internal/observability"
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
	// docs/tutorial/15-observability.md for why it takes one at all.
	var logs bytes.Buffer
	capture := &observability.Logger{Logger: slog.New(slog.NewTextHandler(&logs, nil))}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	n := New(natsbroker.Config{URL: url}, capture)
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

	// Run has returned, which is exactly the state servo calls Shutdown
	// in: the subscription is still open, because tearing it down is
	// Drain's job and closing the connection is Stop's. Both must succeed
	// here, and Drain must not report handlers still running — there are
	// none left.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()
	if err := n.Drain(drainCtx); err != nil {
		t.Errorf("Drain after Run: %v", err)
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Errorf("Stop after Drain: %v", err)
	}
}

// TestDrainWithoutRunIsANoOp: Drain and Stop are reachable without a
// successful Run — if Run never subscribed, or never ran at all, servo
// still calls both during Shutdown. Neither may panic on the nil
// connection that leaves behind.
func TestDrainWithoutRunIsANoOp(t *testing.T) {
	n := New(natsbroker.Config{URL: "nats://unused"}, quietLogger())
	if err := n.Drain(context.Background()); err != nil {
		t.Errorf("Drain before Run: %v", err)
	}
	if err := n.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Run: %v", err)
	}
}

func quietLogger() *observability.Logger {
	return &observability.Logger{Logger: slog.New(slog.DiscardHandler)}
}
