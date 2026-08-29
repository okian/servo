// Package service holds the order service's business logic: it's the only
// package that talks to repository, cache, and broker in the same
// operation, and the only place their interactions have to be thought
// through together.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/repository"
)

type OrderService struct {
	repo      repository.OrderRepository
	cache     cache.OrderCache
	publisher broker.EventPublisher
	log       *observability.Logger
}

func New(repo repository.OrderRepository, c cache.OrderCache, publisher broker.EventPublisher, log *observability.Logger) *OrderService {
	return &OrderService{repo: repo, cache: c, publisher: publisher, log: log}
}

// CreateOrder writes the order first — that's the operation a caller is
// actually waiting on — then treats the cache and the event publish as
// best-effort. A cache-set failure just means the next read falls back to
// Postgres; see docs/tutorial/07-messaging-layer.md for the more
// consequential trade-off a lost publish represents, and why this tutorial
// accepts it rather than solving it.
func (s *OrderService) CreateOrder(ctx context.Context, userID uuid.UUID, item string, quantity int) (*domain.Order, error) {
	if item == "" || quantity <= 0 {
		return nil, fmt.Errorf("%w: item must be non-empty and quantity must be positive", domain.ErrValidation)
	}

	order := &domain.Order{
		ID:        uuid.New(),
		UserID:    userID,
		Item:      item,
		Quantity:  quantity,
		Status:    domain.OrderStatusPending,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("service: create order: %w", err)
	}

	if err := s.cache.Set(ctx, order); err != nil {
		s.log.ErrorContext(ctx, "service: cache set failed after order create", "order_id", order.ID, "error", err)
	}
	if err := s.publisher.PublishOrderPlaced(ctx, order); err != nil {
		s.log.ErrorContext(ctx, "service: publish OrderPlaced failed", "order_id", order.ID, "error", err)
	}
	return order, nil
}

// GetOrder tries the cache first, falls back to the repository on a miss
// (or any other cache error — a degraded cache should never make reads
// fail), and best-effort repopulates the cache after a repository hit.
func (s *OrderService) GetOrder(ctx context.Context, requesterID, orderID uuid.UUID) (*domain.Order, error) {
	if cached, err := s.cache.Get(ctx, orderID); err == nil {
		return authorize(cached, requesterID)
	} else if !errors.Is(err, cache.ErrMiss) {
		s.log.ErrorContext(ctx, "service: cache read failed, falling back to repository", "order_id", orderID, "error", err)
	}

	order, err := s.repo.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if _, err := authorize(order, requesterID); err != nil {
		return nil, err
	}

	if err := s.cache.Set(ctx, order); err != nil {
		s.log.ErrorContext(ctx, "service: cache repopulate failed", "order_id", orderID, "error", err)
	}
	return order, nil
}

func (s *OrderService) ListOrders(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Order, error) {
	return s.repo.ListByUser(ctx, userID, limit, offset)
}

func authorize(o *domain.Order, requesterID uuid.UUID) (*domain.Order, error) {
	if o.UserID != requesterID {
		return nil, domain.ErrForbidden
	}
	return o, nil
}
