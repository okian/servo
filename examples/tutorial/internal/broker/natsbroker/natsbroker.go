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

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/domain"
	"github.com/nats-io/nats.go"
)

// Config owns NATS_URL for the whole messaging layer. The prefix on the
// directive namespaces the environment variable; the generated loader
// (servo_config_gen.go, beside this file) fills the struct and reports a
// missing NATS_URL at startup by name.
//
// The notifier — the consuming end of the same messaging layer — takes
// this Config rather than declaring its own: two structs both tagged to
// read NATS_URL would be two claims on one variable, which `servo
// generate` refuses as a collision. One setting, one owner.
//
//servo:config prefix=NATS
type Config struct {
	URL string `config:"url,required"`
}

type Publisher struct {
	url  string
	conn *nats.Conn
}

var _ broker.EventPublisher = (*Publisher)(nil)

func New(cfg Config) *Publisher {
	return &Publisher{url: cfg.URL}
}

func (p *Publisher) Init(context.Context) error {
	conn, err := nats.Connect(p.url)
	if err != nil {
		return fmt.Errorf("natsbroker: connect: %w", err)
	}
	p.conn = conn
	return nil
}

// Stop tolerates a nil connection because it can be reached without a
// successful Init: if Init fails — NATS unreachable at startup — servo
// rolls the graph back and calls Stop on everything it had already
// constructed, this component included. Dereferencing conn there turns a
// clear "connect: connection refused" into a nil-pointer panic that
// buries it.
func (p *Publisher) Stop(context.Context) error {
	if p.conn == nil {
		return nil
	}
	p.conn.Drain()
	return nil
}

// Health reports unhealthy rather than panicking for the same reason: a
// component whose Init failed still has to answer.
func (p *Publisher) Health(context.Context) error {
	if p.conn == nil {
		return fmt.Errorf("natsbroker: not connected (no connection established)")
	}
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
