// Package resilience wraps existing components with failure-handling
// behavior — a circuit breaker around the cache, a rate limiter in front
// of the API — without either the wrapped component or its caller needing
// to know it's there.
package resilience

import (
	"context"
	"errors"
	"fmt"

	"example.com/servoorders/cache"
	"example.com/servoorders/domain"
	"example.com/servoorders/redis"
	"github.com/google/uuid"
	"github.com/sony/gobreaker/v2"
)

// CircuitBreakerCache implements cache.OrderCache by wrapping *redis.Cache
// specifically — the concrete type, not the cache.OrderCache interface it
// also implements. That's not a style choice: servo.Bind[cache.OrderCache,
// *CircuitBreakerCache]() (added in cmd/orders/spec.go) means the
// interface now resolves to this type, so if this constructor also asked
// for cache.OrderCache, servo would have to resolve that right back to
// itself — a cycle with no real dependency underneath it. Depending on the
// concrete type it wraps is what breaks that; see
// docs/tutorial/13-resilience.md.
type CircuitBreakerCache struct {
	// Stored as the interface, not *redis.Cache, purely so this package's
	// own tests can substitute a mock by constructing this struct directly
	// (whitebox — see breaker_test.go) — *redis.Cache satisfies
	// cache.OrderCache regardless of which type the field is declared as,
	// so this costs nothing at the call sites below.
	next    cache.OrderCache
	breaker *gobreaker.CircuitBreaker[any]
}

func NewCircuitBreakerCache(next *redis.Cache) *CircuitBreakerCache {
	return &CircuitBreakerCache{
		next: next,
		breaker: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
			Name: "cache",
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures > 5
			},
		}),
	}
}

var _ cache.OrderCache = (*CircuitBreakerCache)(nil)

// Get maps an open breaker to cache.ErrMiss rather than a distinct error —
// from the service layer's point of view, "the cache is failing right now"
// and "this key isn't cached" call for the exact same response: fall back
// to the repository. No change to service.OrderService was needed to add
// this.
func (c *CircuitBreakerCache) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	order, err := c.breaker.Execute(func() (any, error) {
		return c.next.Get(ctx, id)
	})
	if errors.Is(err, gobreaker.ErrOpenState) {
		return nil, cache.ErrMiss
	}
	if err != nil {
		return nil, err
	}
	return order.(*domain.Order), nil
}

func (c *CircuitBreakerCache) Set(ctx context.Context, o *domain.Order) error {
	_, err := c.breaker.Execute(func() (any, error) {
		return nil, c.next.Set(ctx, o)
	})
	if err != nil {
		return fmt.Errorf("resilience: %w", err)
	}
	return nil
}

func (c *CircuitBreakerCache) Invalidate(ctx context.Context, id uuid.UUID) error {
	_, err := c.breaker.Execute(func() (any, error) {
		return nil, c.next.Invalidate(ctx, id)
	})
	if err != nil {
		return fmt.Errorf("resilience: %w", err)
	}
	return nil
}
