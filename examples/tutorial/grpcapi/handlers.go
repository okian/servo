package grpcapi

import (
	"context"
	"errors"
	"uuid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"example.com/servoorders/domain"
	"example.com/servoorders/grpcapi/ordersv1"
)

func (s *Server) Login(ctx context.Context, req *ordersv1.LoginRequest) (*ordersv1.LoginResponse, error) {
	token, err := s.auth.Login(ctx, req.GetUsername(), req.GetPassword())
	if err != nil {
		return nil, domainError(err)
	}
	return &ordersv1.LoginResponse{Token: token}, nil
}

func (s *Server) CreateOrder(ctx context.Context, req *ordersv1.CreateOrderRequest) (*ordersv1.Order, error) {
	claims := claimsFrom(ctx)
	order, err := s.orders.CreateOrder(ctx, claims.UserID, req.GetItem(), int(req.GetQuantity()))
	if err != nil {
		return nil, domainError(err)
	}
	return orderProto(order), nil
}

func (s *Server) GetOrder(ctx context.Context, req *ordersv1.GetOrderRequest) (*ordersv1.Order, error) {
	claims := claimsFrom(ctx)

	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid order id")
	}

	order, err := s.orders.GetOrder(ctx, claims.UserID, id)
	if err != nil {
		return nil, domainError(err)
	}

	// Acquire, defer the release, use it — identical to both HTTP
	// variants, because the scope has nothing to do with the transport.
	if sess, release, err := s.sessions.Acquire(ctx); err == nil {
		defer release()
		sess.RecordView(order.ID)
	}

	return orderProto(order), nil
}

func (s *Server) ListOrders(ctx context.Context, req *ordersv1.ListOrdersRequest) (*ordersv1.ListOrdersResponse, error) {
	claims := claimsFrom(ctx)

	limit := int(req.GetLimit())
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	offset := max(int(req.GetOffset()), 0)

	orders, err := s.orders.ListOrders(ctx, claims.UserID, limit, offset)
	if err != nil {
		return nil, domainError(err)
	}

	resp := &ordersv1.ListOrdersResponse{Orders: make([]*ordersv1.Order, len(orders))}
	for i, o := range orders {
		resp.Orders[i] = orderProto(o)
	}
	return resp, nil
}

func (s *Server) Recent(ctx context.Context, _ *ordersv1.RecentRequest) (*ordersv1.RecentResponse, error) {
	sess, release, err := s.sessions.Acquire(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session unavailable")
	}
	defer release()

	recent := sess.Recent()
	out := make([]string, len(recent))
	for i, id := range recent {
		out[i] = id.String()
	}
	return &ordersv1.RecentResponse{Recent: out}, nil
}

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

func orderProto(o *domain.Order) *ordersv1.Order {
	return &ordersv1.Order{
		Id:        o.ID.String(),
		Item:      o.Item,
		Quantity:  int32(o.Quantity),
		Status:    string(o.Status),
		CreatedAt: timestamppb.New(o.CreatedAt),
	}
}

// domainError is this transport's version of writeDomainError: the one
// place a domain sentinel becomes a wire-level code. The mapping differs
// from HTTP's only in vocabulary — NotFound rather than 404 — which is
// exactly why the domain layer names neither.
func domainError(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, domain.ErrForbidden):
		return status.Error(codes.PermissionDenied, "forbidden")
	case errors.Is(err, domain.ErrInvalidCredentials):
		return status.Error(codes.Unauthenticated, "invalid credentials")
	case errors.Is(err, domain.ErrValidation):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
