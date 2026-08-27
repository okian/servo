// Package natsbroker implements broker.EventPublisher against a real NATS
// server. It's not named "nats" — the underlying client library's own
// package is already called that, and a component importing both its own
// domain type and the client library would otherwise need an import alias
// on one of them for no real benefit.
package natsbroker

import (
	"context"
	"encoding/json"
	"fmt"

	"example.com/servoorders/broker"
	"example.com/servoorders/config"
	"example.com/servoorders/domain"
	"github.com/nats-io/nats.go"
)

type Publisher struct {
	url  string
	conn *nats.Conn
}

var _ broker.EventPublisher = (*Publisher)(nil)

func New(cfg *config.Config) *Publisher {
	return &Publisher{url: cfg.NATSURL}
}

func (p *Publisher) Init(context.Context) error {
	conn, err := nats.Connect(p.url)
	if err != nil {
		return fmt.Errorf("natsbroker: connect: %w", err)
	}
	p.conn = conn
	return nil
}

func (p *Publisher) Stop(context.Context) error {
	p.conn.Drain()
	return nil
}

func (p *Publisher) Health(context.Context) error {
	if !p.conn.IsConnected() {
		return fmt.Errorf("natsbroker: not connected (status: %s)", p.conn.Status())
	}
	return nil
}

func (p *Publisher) PublishOrderPlaced(ctx context.Context, o *domain.Order) error {
	raw, err := json.Marshal(broker.OrderPlacedEvent{
		OrderID: o.ID.String(),
		UserID:  o.UserID.String(),
		Item:    o.Item,
	})
	if err != nil {
		return fmt.Errorf("natsbroker: marshal: %w", err)
	}
	if err := p.conn.Publish(broker.OrderPlacedSubject, raw); err != nil {
		return fmt.Errorf("natsbroker: publish: %w", err)
	}
	return nil
}
