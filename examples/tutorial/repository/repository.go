// Package repository declares the persistence boundary as interfaces. The
// service layer depends on these, never on postgres directly — that
// indirection is what makes servotest.Override-based mocking possible
// without the service layer knowing or caring.
package repository

import (
	"context"

	"example.com/servoorders/domain"
	"github.com/google/uuid"
)

type OrderRepository interface {
	Create(ctx context.Context, o *domain.Order) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Order, error)
}

type UserRepository interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
}
