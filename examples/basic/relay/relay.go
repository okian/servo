// Package relay demonstrates needing two instances of the same underlying
// client type at once: it forwards an order event from the orders AWS
// account's queue into the audit account's queue, a realistic reason one
// component needs two distinct accounts simultaneously.
package relay

import (
	"context"
	"log"

	"example.com/servobasic/queue"
)

type Relay struct {
	orders *queue.OrdersAccount
	audit  *queue.AuditAccount

	// Set by Init; exported so tests can assert on them directly instead
	// of capturing log output.
	OrdersResult string
	AuditResult  string
}

func New(orders *queue.OrdersAccount, audit *queue.AuditAccount) *Relay {
	return &Relay{orders: orders, audit: audit}
}

func (r *Relay) Init(ctx context.Context) error {
	r.OrdersResult = r.orders.Send("order-created")
	r.AuditResult = r.audit.Send("order-created (audit copy)")
	log.Println(r.OrdersResult)
	log.Println(r.AuditResult)
	return nil
}
