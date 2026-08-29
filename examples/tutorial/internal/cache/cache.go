// Package cache declares the caching boundary the service layer reads and
// writes through. It never returns domain.ErrNotFound — a cache miss and
// "this order doesn't exist" are different situations the service layer
// has to tell apart, so a miss gets its own sentinel instead.
package cache

import (
	"context"
	"errors"
	"uuid"

	"example.com/servoorders/internal/domain"
)

var ErrMiss = errors.New("cache: miss")

type OrderCache interface {
	Get(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	Set(ctx context.Context, o *domain.Order) error
	Invalidate(ctx context.Context, id uuid.UUID) error
}
