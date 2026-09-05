package natsbroker

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/domain"
	"github.com/nats-io/nats.go"
)

// testPublisher skips unless TEST_NATS_URL is set — start
// deploy/docker-compose.yml's nats service (`make up`) to run this for
// real. See docs/tutorial/07-messaging-layer.md.
func testPublisher(t *testing.T) (*Publisher, string) {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set; see docs/tutorial/07-messaging-layer.md")
	}

	p := New(Config{URL: url})
	if err := p.Init(context.Background()); err != nil {
		t.Fatalf("Init (is NATS running at %s?): %v", url, err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	return p, url
}

func TestPublishOrderPlacedIsReceivedBySubscribers(t *testing.T) {
	p, url := testPublisher(t)

	// A second, independent connection plays the role of notifier here —
	// the point is proving Publish actually reaches the wire, not
	// exercising notifier's own code (that's notifier's own test).
	sub, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect subscriber: %v", err)
	}
	defer sub.Close()

	received := make(chan broker.OrderPlacedEvent, 1)
	subscription, err := sub.Subscribe(broker.OrderPlacedSubject, func(msg *nats.Msg) {
		var event broker.OrderPlacedEvent
		if err := json.Unmarshal(msg.Data, &event); err == nil {
			received <- event
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer subscription.Unsubscribe()
	if err := sub.Flush(); err != nil { // ensures the subscription is registered before we publish
		t.Fatalf("flush: %v", err)
	}

	order := &domain.Order{ID: uuid.New(), UserID: uuid.New(), Item: "widget"}
	if err := p.PublishOrderPlaced(context.Background(), order); err != nil {
		t.Fatalf("PublishOrderPlaced: %v", err)
	}

	select {
	case event := <-received:
		if event.OrderID != order.ID.String() || event.Item != "widget" {
			t.Errorf("received %+v, want OrderID=%s Item=widget", event, order.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}
}

func TestHealthReflectsConnectionState(t *testing.T) {
	p, _ := testPublisher(t)

	if err := p.Health(context.Background()); err != nil {
		t.Errorf("Health while connected = %v, want nil", err)
	}
}
