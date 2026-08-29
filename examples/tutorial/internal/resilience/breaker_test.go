package resilience

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/mocks"
	"github.com/sony/gobreaker/v2"
	"go.uber.org/mock/gomock"
)

// newTestBreakerCache builds a CircuitBreakerCache around a mock directly
// — bypassing NewCircuitBreakerCache's *redis.Cache parameter, which a
// gomock double can't satisfy. This only works because this file is
// package resilience (whitebox), not resilience_test; an external test
// couldn't reach the unexported next field this way.
func newTestBreakerCache(t *testing.T, tripAfter uint32) (*CircuitBreakerCache, *mocks.MockOrderCache) {
	t.Helper()
	mock := mocks.NewMockOrderCache(gomock.NewController(t))
	return &CircuitBreakerCache{
		next: mock,
		breaker: gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
			Name: "test",
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures >= tripAfter
			},
		}),
	}, mock
}

func TestCircuitBreakerCachePassesThroughWhenClosed(t *testing.T) {
	bc, mock := newTestBreakerCache(t, 3)
	order := &domain.Order{ID: uuid.New(), Item: "widget"}
	mock.EXPECT().Get(gomock.Any(), order.ID).Return(order, nil)

	got, err := bc.Get(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != order {
		t.Errorf("Get returned %+v, want %+v", got, order)
	}
}

func TestCircuitBreakerCacheOpensAfterConsecutiveFailuresAndReportsAMiss(t *testing.T) {
	bc, mock := newTestBreakerCache(t, 2)
	failure := errors.New("redis: connection refused")
	id := uuid.New()

	// Two real failures trip the breaker (ReadyToTrip fires on the second).
	mock.EXPECT().Get(gomock.Any(), id).Return(nil, failure).Times(2)
	for range 2 {
		if _, err := bc.Get(context.Background(), id); !errors.Is(err, failure) {
			t.Fatalf("Get before the breaker trips: err = %v, want %v", err, failure)
		}
	}

	// The breaker is now open: no third call should ever reach the mock —
	// there is deliberately no third .EXPECT() above, so gomock itself
	// fails the test if Get calls through anyway.
	got, err := bc.Get(context.Background(), id)
	if !errors.Is(err, cache.ErrMiss) {
		t.Errorf("Get with an open breaker: err = %v, want cache.ErrMiss", err)
	}
	if got != nil {
		t.Errorf("Get with an open breaker: got = %v, want nil", got)
	}
}
