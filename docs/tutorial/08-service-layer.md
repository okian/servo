# 8. Service layer

Every layer so far has been a single, focused concern — one interface, one implementation, one
job. This chapter is different: the service layer is the one place that talks to the repository,
the cache, and the broker *in the same operation*, and the only place their interactions actually
have to be thought through together. It's also the layer everything before it was building toward,
and — because it depends only on interfaces — the first one we can unit-test without a database,
cache, or broker anywhere in sight.

## Wire up the three dependencies

Create `service/service.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/repository"
)

type OrderService struct {
	repo      repository.OrderRepository
	cache     cache.OrderCache
	publisher broker.EventPublisher
	log       *observability.Logger
}

func New(
	repo repository.OrderRepository,
	c cache.OrderCache,
	publisher broker.EventPublisher,
	log *observability.Logger,
) *OrderService {
	return &OrderService{repo: repo, cache: c, publisher: publisher, log: log}
}
```

`*observability.Logger` is built in [chapter 15](15-observability.md) and threaded through here
because everything that logs takes one, rather than reaching for the package-level `slog`
functions. That is a deliberate choice with a real argument behind it — see
[Why the logger is a node](15-observability.md#why-the-logger-is-a-node-and-not-a-call-at-the-top-of-main)
— and it is why this constructor has a parameter it does not appear to use until the error paths
below.

Notice what `New` takes: three interfaces, not three concrete types. `OrderService` has never
heard of `postgres`, `redis`, or `natsbroker`, and never will — everything it needs to know about
each dependency, it already declared for itself back in chapters 5 through 7. That's the payoff of
writing the interfaces first arriving all at once, right here.

## Write CreateOrder, and decide what "best-effort" means

Start with validation — reject bad input before touching anything:

```go
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
```

Up to here, every failure is a real failure — bad input, or the database write itself failing, and
both correctly fail the whole request. What happens next is where a real decision has to be made.
Once the order is safely in Postgres, should a failure to update the cache, or to publish the
`OrderPlaced` event, also fail the request the caller is waiting on? Add this and then look closely
at what it's actually saying:

```go
	if err := s.cache.Set(ctx, order); err != nil {
		s.log.ErrorContext(ctx, "service: cache set failed after order create", "order_id", order.ID, "error", err)
	}
	if err := s.publisher.PublishOrderPlaced(ctx, order); err != nil {
		s.log.ErrorContext(ctx, "service: publish OrderPlaced failed", "order_id", order.ID, "error", err)
	}
	return order, nil
}
```

Both failures are logged, and neither one returns an error. That's a deliberate choice, not a
missed error check: the guarantee `CreateOrder` actually makes is "the order was durably saved,"
and it was — a cold cache just means the next read costs one more Postgres query, no different
from any cache miss. A lost event is a more serious gap ([chapter 7](07-messaging-layer.md) already
walked through why), but even there, telling the caller their order failed when it didn't would be
the wrong trade — they'd retry, and now there are two orders. Accepting a known, explained gap
turned out to be better than pretending we could return a single error for two independent
failures anyway.

## Write GetOrder: try the cache, fall back, repopulate

```go
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
```

The `else if !errors.Is(err, cache.ErrMiss)` branch is worth reading twice. A miss is the expected,
silent case — of course it falls through to Postgres, that's what a cache miss means. Anything
*else* coming back from the cache (Redis unreachable, a corrupt value) also falls through to
Postgres, but logs first — the read still succeeds, degraded rather than broken, which is exactly
the "cache is an optimization, not a dependency" principle from [chapter 6](06-caching-layer.md)
actually paying off here. Notice too that `Get` on the repository is called with no authorization
check first, and `authorize` runs *after* — that's intentional: fetching the wrong order and then
rejecting it is fine; leaking whether an order ID exists at all to someone who doesn't own it is
not something this shape has to worry about, since both "not found" and "not yours" are real,
separate errors the API layer will map to 404 and 403 respectively.

## Set up mocks and prove all of this works, with nothing running

This is the payoff for every interface written since chapter 5: `OrderService` can be tested
completely, with no Postgres, no Redis, no NATS anywhere in the process. We'll use
[gomock](https://github.com/uber-go/mock), the same tool servo's own `examples/mocking/gomock`
uses. Create `mocks/gen.go`:

```go
// Package mocks holds gomock-generated doubles for the three interfaces the
// service layer depends on. Regenerate with `go generate ./mocks/...` after
// changing any of the three source interfaces.
package mocks

//go:generate go run go.uber.org/mock/mockgen -source=../repository/repository.go -destination=repository_mock.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../cache/cache.go -destination=cache_mock.go -package=mocks
//go:generate go run go.uber.org/mock/mockgen -source=../broker/broker.go -destination=broker_mock.go -package=mocks
```

Add `mockgen` as a tool dependency — this is what lets `go run go.uber.org/mock/mockgen` above work
from a fresh clone with no separate install step, and what keeps its own transitive dependencies in
`go.sum` even though nothing else in the module imports it directly:

```
$ go get -tool go.uber.org/mock/mockgen
$ go generate ./mocks/...
```

That produces `mocks/repository_mock.go`, `mocks/cache_mock.go`, and `mocks/broker_mock.go` — real
generated code, committed like any other generated file, regenerated whenever the interfaces
change rather than hand-edited.

Now write the tests, in `service/service_test.go`. The first one exercises the ordinary path:

```go
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
```

`gomock.NewController(t)` — with a real `*testing.T`, since this is an ordinary test function, not
code running inside servo's generated graph — is simpler than the `servotest.PanicReporter`
pattern servo's mocking examples use. That pattern earns its keep specifically when a mock has to
be constructed *inside* the graph itself, with no `*testing.T` reachable at all; you'll see exactly
that situation in [chapter 13](13-wiring-with-servo.md#capabilities-side-by-side). Here, testing
`OrderService` directly, the simple form is all you need.

The next two tests are where mocks genuinely earn their keep over a real database — proving a
specific failure mode is trivial when you control every call directly:

```go
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
```

`TestCreateOrderPersistsCachesAndPublishes` above is really about the ordinary path; this second
test is specifically about the `err != nil` branch that logs and moves on — the exact decision from
the `CreateOrder` walkthrough above, now pinned down by a test instead of just a comment. It
couldn't be simpler to write: tell the mock to return an error, and check the method still
succeeds. The rest of the suite (in the real file) covers authorization, cache hits, and
not-found — every branch `CreateOrder` and `GetOrder` can take.

## Run it

```
$ go test ./service/... -v
=== RUN   TestCreateOrderPersistsCachesAndPublishes
--- PASS: TestCreateOrderPersistsCachesAndPublishes (0.00s)
=== RUN   TestCreateOrderRejectsInvalidInput
--- PASS: TestCreateOrderRejectsInvalidInput (0.00s)
=== RUN   TestCreateOrderSucceedsEvenIfPublishFails
2026/08/27 13:27:30 ERROR service: publish OrderPlaced failed order_id=0fb62f68-6e36-4a71-b59c-447e5ca612d7 error="nats: no responders"
--- PASS: TestCreateOrderSucceedsEvenIfPublishFails (0.00s)
=== RUN   TestGetOrderReturnsCachedValueOnHit
--- PASS: TestGetOrderReturnsCachedValueOnHit (0.00s)
=== RUN   TestGetOrderFallsBackToRepositoryOnCacheMiss
--- PASS: TestGetOrderFallsBackToRepositoryOnCacheMiss (0.00s)
=== RUN   TestGetOrderRejectsAnotherUsersOrder
--- PASS: TestGetOrderRejectsAnotherUsersOrder (0.00s)
=== RUN   TestGetOrderPassesThroughNotFound
--- PASS: TestGetOrderPassesThroughNotFound (0.00s)
PASS
ok  	example.com/servoorders/internal/service	0.127s
```

That `ERROR` line in the middle isn't a failure — it's `slog`'s default logger, doing exactly what
`CreateOrder` told it to do when the mock publisher returned an error. The test still passes,
because it's checking the return value, not the absence of a log line. [Chapter
15](15-observability.md) is where that logger stops being the unconfigured default and starts
looking like something you'd actually want in production.

## Diagnostics

- **`missing call(s) to *MockX.Y`**, from gomock, at test cleanup — you set up an `.EXPECT()` that
  never happened. Either the code path you expected to run didn't, or you're asserting on a mock
  method the code under test doesn't actually call for that input.
- **A test passes locally but the mock's `EXPECT()` order matters in a way you didn't intend** —
  gomock doesn't enforce call order by default; if a test genuinely needs `Create` before
  `Set` before `Publish`, use `gomock.InOrder(...)` explicitly rather than relying on incidental
  ordering.
- **`mocks/repository_mock.go` is out of date after changing an interface** — re-run `go generate
  ./mocks/...`. There's no watcher catching this automatically; a stale mock still compiles, so
  the only signal is a test failing (or worse, silently testing the wrong thing) until you
  regenerate.

## Do's and don'ts

- **Do** keep `s.log.ErrorContext` (or any logging) out of the *validation* path — an invalid
  request from a client isn't a service-side failure worth logging as an error; it's the caller's
  input being wrong, which is exactly what returning `domain.ErrValidation` already communicates.
- **Do** let the service layer be the only place that decides what's "best-effort" versus
  "must succeed." Pushing that judgment down into `redis` or `natsbroker` would mean the
  repository, cache, and broker packages each have to guess how important they are to the caller —
  they can't know that; only the operation that uses all three can.
- **Don't** let `OrderService` reach for `context.Background()` internally instead of using the
  `ctx` it was given. The context a handler receives carries the request's deadline and
  cancellation — dropping it anywhere in this chain means a client that gave up waiting doesn't
  actually stop work from happening on their behalf.
- **Don't** test `OrderService` by spinning up real Postgres/Redis/NATS. That's what
  [chapter 17](17-testing-strategy.md)'s integration and API tiers are for — a unit test that needs
  Docker running isn't a unit test anymore, and the whole reason to write interfaces in chapters 5
  through 7 was to make this layer testable without any of that.

## Next

[Chapter 9: Authentication](09-authentication.md) — JWTs, password hashing, and the middleware
that will protect every order endpoint.
