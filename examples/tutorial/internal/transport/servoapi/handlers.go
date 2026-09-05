package servoapi

import (
	"context"
	"uuid"

	"github.com/okian/servo/v3/servo"

	"example.com/servoorders/internal/auth"
	"example.com/servoorders/internal/service"
	"example.com/servoorders/internal/session"
)

// Login is the one unauthenticated route — it is simply not in Auth's
// servo.Use selector list, and it takes no auth.Claims parameter, so the
// extractor never runs for it either.
//
//servo:post /auth/login
func Login(ctx context.Context, req *LoginReq, authSvc *service.AuthService) (servo.Json[*LoginResp], error) {
	token, err := authSvc.Login(ctx, req.Username, req.Password)
	if err != nil {
		return nil, statusFor(err)
	}
	return servo.JSON(&LoginResp{Token: token}), nil
}

// CreateOrder shows the whole contract at once: a decoded body, an
// extracted auth.Claims, a graph-resolved service, and 201 picked by the
// status in the error position.
//
//servo:post /orders
func CreateOrder(ctx context.Context, req *CreateOrderReq, claims auth.Claims, orders *service.OrderService) (servo.Json[*OrderResp], error) {
	order, err := orders.CreateOrder(ctx, claims.UserID, req.Item, req.Quantity)
	if err != nil {
		return nil, statusFor(err)
	}
	return servo.JSON(NewOrderResp(order)), servo.Status.CREATED
}

// GetOrder still records the view on the caller's session — through the
// accessor, per request, exactly as the hand-written handler does. A
// *session.Session parameter would be the widening bug, and `servo
// generate` refuses to emit it.
//
//servo:get /orders/{id}
func GetOrder(ctx context.Context, req *GetOrderReq, claims auth.Claims, orders *service.OrderService, sessions session.Sessions) (servo.Json[*OrderResp], error) {
	id, err := uuid.Parse(req.ID)
	if err != nil {
		return nil, servo.Status.BAD_REQUEST.New("invalid order id")
	}
	order, err := orders.GetOrder(ctx, claims.UserID, id)
	if err != nil {
		return nil, statusFor(err)
	}
	if sess, release, err := sessions.Acquire(ctx); err == nil {
		defer release()
		sess.RecordView(order.ID)
	}
	return servo.JSON(NewOrderResp(order)), nil
}

//servo:get /orders
func ListOrders(ctx context.Context, req *ListOrdersReq, claims auth.Claims, orders *service.OrderService) (servo.Json[*ListOrdersResp], error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := max(req.Offset, 0)

	list, err := orders.ListOrders(ctx, claims.UserID, limit, offset)
	if err != nil {
		return nil, statusFor(err)
	}
	resp := &ListOrdersResp{Orders: make([]*OrderResp, len(list))}
	for i, o := range list {
		resp.Orders[i] = NewOrderResp(o)
	}
	return servo.JSON(resp), nil
}

// Recent is unchanged in spirit from the hand-written handler: the session
// is the storage, and the accessor is how a per-request instance reaches a
// generated, refcounted one.
//
//servo:get /me/recent
func Recent(ctx context.Context, claims auth.Claims, sessions session.Sessions) (servo.Json[*RecentResp], error) {
	sess, release, err := sessions.Acquire(ctx)
	if err != nil {
		return nil, servo.Status.INTERNAL_SERVER_ERROR.Wrap(err)
	}
	defer release()
	return servo.JSON(&RecentResp{Recent: sess.Recent()}), nil
}
