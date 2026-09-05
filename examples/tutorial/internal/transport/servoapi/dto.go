// Package servoapi is the fourth transport: the same API as api, ginapi
// and grpcapi, declared as //servo: directives instead of written as a
// server. The router, request decoding, response encoding and listener
// lifecycle are generated; what remains here is exactly the part that was
// ever specific to this service — the shapes, the handlers, and the one
// place a domain error becomes a status.
//
// Unlike api's DTOs, these are exported: the generated adapters live in the
// injector package (cmd/ordersservo) and construct the request structs by
// name.
package servoapi

import (
	"errors"
	"time"
	"uuid"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/domain"
)

type LoginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResp struct {
	Token string `json:"token"`
}

type CreateOrderReq struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

type GetOrderReq struct {
	ID string `path:"id"`
}

type ListOrdersReq struct {
	Limit  int `query:"limit"`
	Offset int `query:"offset"`
}

type OrderResp struct {
	ID        uuid.UUID `json:"id"`
	Item      string    `json:"item"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func NewOrderResp(o *domain.Order) *OrderResp {
	return &OrderResp{
		ID:        o.ID,
		Item:      o.Item,
		Quantity:  o.Quantity,
		Status:    string(o.Status),
		CreatedAt: o.CreatedAt,
	}
}

type ListOrdersResp struct {
	Orders []*OrderResp `json:"orders"`
}

type RecentResp struct {
	Recent []uuid.UUID `json:"recent"`
}

// statusFor is api.writeDomainError, one transport later: the same single
// point where a domain sentinel becomes an HTTP status — except it returns
// a status-carrying error for the generated adapter to write, instead of
// writing the response itself. Everything below the API layer still deals
// only in domain errors.
func statusFor(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return servo.Status.NOT_FOUND.New("not found")
	case errors.Is(err, domain.ErrForbidden):
		return servo.Status.FORBIDDEN.New("access forbidden")
	case errors.Is(err, domain.ErrValidation):
		return servo.Status.BAD_REQUEST.Wrap(err)
	case errors.Is(err, domain.ErrInvalidCredentials):
		return servo.Status.UNAUTHORIZED.New("invalid credentials")
	default:
		return servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)
	}
}
