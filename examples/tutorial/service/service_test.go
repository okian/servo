package service_test

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
	"example.com/servoorders/mocks"
	"example.com/servoorders/service"
	"go.uber.org/mock/gomock"
)

// These tests never touch a real database, cache, or broker — that's the
// entire point of depending on interfaces (chapter 5) instead of postgres,
// redis, and natsbroker directly. gomock.NewController(t) here, with a
// real *testing.T, is simpler than servotest.PanicReporter's zero-arg
// pattern; that pattern only earns its keep once a mock has to be
// constructed from inside servo's generated graph (chapter 11), where no
// *testing.T is reachable at all.

func TestCreateOrderPersistsCachesAndPublishes(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	c.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil)
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(nil)

	svc := service.New(repo, c, pub)
	userID := uuid.New()
	order, err := svc.CreateOrder(context.Background(), userID, "widget", 3)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if order.UserID != userID || order.Item != "widget" || order.Quantity != 3 {
		t.Errorf("CreateOrder returned %+v, want a matching order", order)
	}
	if order.Status != domain.OrderStatusPending {
		t.Errorf("Status = %v, want %v", order.Status, domain.OrderStatusPending)
	}
}

func TestCreateOrderRejectsInvalidInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	// No .EXPECT() calls at all: an invalid request must fail validation
	// before touching the repository, cache, or publisher — gomock fails
	// the test if any unexpected call reaches these mocks.
	svc := service.New(
		mocks.NewMockOrderRepository(ctrl),
		mocks.NewMockOrderCache(ctrl),
		mocks.NewMockEventPublisher(ctrl),
	)

	if _, err := svc.CreateOrder(context.Background(), uuid.New(), "", 1); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("empty item: err = %v, want domain.ErrValidation", err)
	}
	if _, err := svc.CreateOrder(context.Background(), uuid.New(), "widget", 0); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("zero quantity: err = %v, want domain.ErrValidation", err)
	}
}

// A failed publish must not fail the request: the order was created
// successfully, and that's the guarantee CreateOrder actually makes. See
// docs/tutorial/07-messaging-layer.md for the trade-off this represents.
func TestCreateOrderSucceedsEvenIfPublishFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	repo.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	c.EXPECT().Set(gomock.Any(), gomock.Any()).Return(nil)
	pub.EXPECT().PublishOrderPlaced(gomock.Any(), gomock.Any()).Return(errors.New("nats: no responders"))

	svc := service.New(repo, c, pub)
	if _, err := svc.CreateOrder(context.Background(), uuid.New(), "widget", 1); err != nil {
		t.Fatalf("CreateOrder: %v, want nil despite the publish failure", err)
	}
}

func TestGetOrderReturnsCachedValueOnHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	userID := uuid.New()
	orderID := uuid.New()
	cached := &domain.Order{ID: orderID, UserID: userID, Item: "widget", Quantity: 1}
	c.EXPECT().Get(gomock.Any(), orderID).Return(cached, nil)
	// repo.Get must NOT be called on a cache hit — no .EXPECT() for it.

	svc := service.New(repo, c, pub)
	got, err := svc.GetOrder(context.Background(), userID, orderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got != cached {
		t.Errorf("GetOrder returned %+v, want the cached value %+v", got, cached)
	}
}

func TestGetOrderFallsBackToRepositoryOnCacheMiss(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	userID := uuid.New()
	orderID := uuid.New()
	stored := &domain.Order{ID: orderID, UserID: userID, Item: "widget", Quantity: 1}
	c.EXPECT().Get(gomock.Any(), orderID).Return(nil, cache.ErrMiss)
	repo.EXPECT().Get(gomock.Any(), orderID).Return(stored, nil)
	c.EXPECT().Set(gomock.Any(), stored).Return(nil) // best-effort repopulate

	svc := service.New(repo, c, pub)
	got, err := svc.GetOrder(context.Background(), userID, orderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got != stored {
		t.Errorf("GetOrder returned %+v, want the repository value %+v", got, stored)
	}
}

func TestGetOrderRejectsAnotherUsersOrder(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	owner := uuid.New()
	requester := uuid.New()
	orderID := uuid.New()
	c.EXPECT().Get(gomock.Any(), orderID).Return(&domain.Order{ID: orderID, UserID: owner}, nil)

	svc := service.New(repo, c, pub)
	if _, err := svc.GetOrder(context.Background(), requester, orderID); !errors.Is(err, domain.ErrForbidden) {
		t.Errorf("GetOrder for another user's order: err = %v, want domain.ErrForbidden", err)
	}
}

func TestGetOrderPassesThroughNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	repo := mocks.NewMockOrderRepository(ctrl)
	c := mocks.NewMockOrderCache(ctrl)
	pub := mocks.NewMockEventPublisher(ctrl)

	orderID := uuid.New()
	c.EXPECT().Get(gomock.Any(), orderID).Return(nil, cache.ErrMiss)
	repo.EXPECT().Get(gomock.Any(), orderID).Return(nil, domain.ErrNotFound)

	svc := service.New(repo, c, pub)
	if _, err := svc.GetOrder(context.Background(), uuid.New(), orderID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetOrder for a missing order: err = %v, want domain.ErrNotFound", err)
	}
}
