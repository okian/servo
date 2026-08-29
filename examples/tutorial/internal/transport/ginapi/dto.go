package ginapi

import (
	"time"
	"uuid"

	"example.com/servoorders/internal/domain"
)

// The request and response shapes are declared here rather than shared
// with the net/http variant in api/. Each transport package is meant to
// be readable on its own — that is the point of having two — and a
// shared DTO package would put the one thing a reader most wants to
// compare in a third file neither of them shows.

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type loginResponse struct {
	Token string `json:"token"`
}

// createOrderRequest carries binding tags, which is the first real
// difference from the net/http variant: Gin validates the shape before a
// handler runs, so the handler never checks for an empty item itself.
// Quantity has no `min` tag on purpose — the domain layer owns that rule,
// and duplicating it here would mean two places to change it.
type createOrderRequest struct {
	Item     string `json:"item" binding:"required"`
	Quantity int    `json:"quantity"`
}

type orderResponse struct {
	ID        uuid.UUID `json:"id"`
	Item      string    `json:"item"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func newOrderResponse(o *domain.Order) orderResponse {
	return orderResponse{
		ID:        o.ID,
		Item:      o.Item,
		Quantity:  o.Quantity,
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	}
}

type listOrdersResponse struct {
	Orders []orderResponse `json:"orders"`
}

type recentResponse struct {
	Recent []uuid.UUID `json:"recent"`
}

type errorResponse struct {
	Error string `json:"error"`
}
