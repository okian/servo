// Package domain holds the order service's core types. Nothing here imports
// net/http, pgx, or servo — a change to how orders are stored or served
// should never require a change here, and vice versa.
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const OrderStatusPending OrderStatus = "pending"

type Order struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Item      string
	Quantity  int
	Status    OrderStatus
	CreatedAt time.Time
}

type User struct {
	ID           uuid.UUID
	Username     string
	PasswordHash string
}

// These are sentinel errors, not HTTP status codes or gRPC codes — domain
// and repository code should never know which transport is listening.
// api.writeDomainError (see the api layer) is the one place that
// translates these into a wire format.
var (
	ErrNotFound           = errors.New("resource not found")
	ErrForbidden          = errors.New("access forbidden")
	ErrValidation         = errors.New("validation failed")
	ErrInvalidCredentials = errors.New("invalid credentials")
)
