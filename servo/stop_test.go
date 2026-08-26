package servo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunStopSuccess(t *testing.T) {
	res := RunStop(context.Background(), time.Second, "x", func(ctx context.Context) error {
		return nil
	})
	if res.Status != StatusOK {
		t.Fatalf("got %+v, want ok", res)
	}
}

func TestRunStopFailure(t *testing.T) {
	want := errors.New("boom")
	res := RunStop(context.Background(), time.Second, "x", func(ctx context.Context) error {
		return want
	})
	if res.Status != StatusFailed || !errors.Is(res.Err, want) {
		t.Fatalf("got %+v, want failed wrapping %v", res, want)
	}
}

func TestRunStopAbandoned(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	res := RunStop(context.Background(), 10*time.Millisecond, "slow", func(ctx context.Context) error {
		<-release // never returns within the budget
		return nil
	})
	if res.Status != StatusAbandoned {
		t.Fatalf("got %+v, want abandoned", res)
	}
}

func TestRunStopGoroutineDoesNotLeakAfterAbandon(t *testing.T) {
	// The done channel must be buffered so the goroutine's send does not
	// block forever once RunStop has already returned on timeout.
	proceed := make(chan struct{})
	finished := make(chan struct{})

	res := RunStop(context.Background(), 5*time.Millisecond, "slow", func(ctx context.Context) error {
		<-proceed
		close(finished)
		return nil
	})
	if res.Status != StatusAbandoned {
		t.Fatalf("got %+v, want abandoned", res)
	}

	close(proceed)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("goroutine appears leaked/blocked after abandonment")
	}
}
