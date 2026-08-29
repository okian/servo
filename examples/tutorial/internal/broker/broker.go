// Package broker declares the messaging boundary: the event the service
// layer publishes, the subject name both the publisher and the subscriber
// (see notifier) agree on, and the interface the service layer publishes
// through.
package broker

import (
	"context"

	"example.com/servoorders/internal/domain"
)

const OrderPlacedSubject = "orders.placed"

type OrderPlacedEvent struct {
	OrderID string `json:"order_id"`
	UserID  string `json:"user_id"`
	Item    string `json:"item"`
}

type EventPublisher interface {
	PublishOrderPlaced(ctx context.Context, o *domain.Order) error
}
