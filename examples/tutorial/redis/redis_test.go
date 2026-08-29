package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"uuid"

	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
)

// testCache skips unless TEST_REDIS_ADDR is set — start
// deploy/docker-compose.yml's redis service (`make up`) to run these for
// real. See docs/tutorial/06-caching-layer.md.
func testCache(t *testing.T) *Cache {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set; see docs/tutorial/06-caching-layer.md")
	}

	c := New(&Config{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Init(ctx); err != nil {
		t.Fatalf("Init (is Redis running at %s?): %v", addr, err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	return c
}

func TestGetOnEmptyKeyReturnsErrMiss(t *testing.T) {
	c := testCache(t)

	_, err := c.Get(context.Background(), uuid.New())
	if !errors.Is(err, cache.ErrMiss) {
		t.Errorf("Get on an unset key = %v, want cache.ErrMiss", err)
	}
}

func TestSetThenGetRoundTrips(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()

	order := &domain.Order{
		ID:        uuid.New(),
		UserID:    uuid.New(),
		Item:      "widget",
		Quantity:  2,
		Status:    domain.OrderStatusPending,
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond), // JSON round-trips to millisecond precision
	}
	if err := c.Set(ctx, order); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := c.Get(ctx, order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Item != order.Item || got.Quantity != order.Quantity || !got.CreatedAt.Equal(order.CreatedAt) {
		t.Errorf("Get returned %+v, want a match for %+v", got, order)
	}
}

func TestInvalidateRemovesTheKey(t *testing.T) {
	c := testCache(t)
	ctx := context.Background()

	order := &domain.Order{ID: uuid.New(), Item: "widget", Quantity: 1}
	if err := c.Set(ctx, order); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Invalidate(ctx, order.ID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if _, err := c.Get(ctx, order.ID); !errors.Is(err, cache.ErrMiss) {
		t.Errorf("Get after Invalidate = %v, want cache.ErrMiss", err)
	}
}
