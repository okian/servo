package api

import (
	"time"
	"uuid"

	"example.com/servoorders/domain"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

type createOrderRequest struct {
	Item     string `json:"item"`
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
