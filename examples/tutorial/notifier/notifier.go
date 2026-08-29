// Package notifier is the consuming side of the messaging layer: it
// subscribes to broker.OrderPlacedSubject and logs each event. A real
// second service would do this in its own process; this exists in the same
// binary purely to make the event-driven half of the architecture visible
// and testable without standing up an actual second service.
package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"example.com/servoorders/broker"
	"example.com/servoorders/config"
	"github.com/nats-io/nats.go"
)

// envPrefix is the same one natsbroker uses: both ends of the messaging
// layer connect to the same server, and each says so for itself rather
// than sharing a struct to agree on it.
const envPrefix = "NATS_"

type Config struct {
	URL string `env:"URL,required"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

type Notifier struct {
	url string
}

func New(cfg *Config) *Notifier {
	return &Notifier{url: cfg.URL}
}

// Run connects and subscribes on its own — not in a separate Init — since
// there's nothing useful to do with the subscription except hold it open
// until shutdown; a Runner-only capability, no Initializer.
func (n *Notifier) Run(ctx context.Context) error {
	conn, err := nats.Connect(n.url)
	if err != nil {
		return fmt.Errorf("notifier: connect: %w", err)
	}
	defer conn.Drain()

	sub, err := conn.Subscribe(broker.OrderPlacedSubject, func(msg *nats.Msg) {
		var event broker.OrderPlacedEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			slog.ErrorContext(ctx, "notifier: malformed event", "error", err)
			return
		}
		slog.InfoContext(ctx, "order placed",
			"order_id", event.OrderID, "user_id", event.UserID, "item", event.Item)
	})
	if err != nil {
		return fmt.Errorf("notifier: subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}
