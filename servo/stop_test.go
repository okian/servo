package servo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// panicDuringStop stands in for the shutdown method that blows up in
// practice: a Close that touches something the node already let go of. It
// is a named function rather than an inline closure so the assertions on
// the recovered stack can name the frame that actually panicked — showing
// that frame is the only reason RunStop captures a stack at all.
func panicDuringStop(context.Context) error {
	panic("close of closed database")
}

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

// TestRunStopReportsAPanicAsAFailedNode covers why the recover in RunStop
// has to exist at all: the panic happens on servo's goroutine, not the
// caller's, so no recover in main can reach it. Without this the process
// dies mid-teardown with every node behind this one still running and no
// Report to say which one did it. It has to come back failed rather than
// abandoned — abandoned means "still running, we stopped waiting", and a
// node that panicked is finished, just badly.
func TestRunStopReportsAPanicAsAFailedNode(t *testing.T) {
	// A budget far longer than the call needs, so an abandoned result here
	// would mean the recover never managed to send and the select fell
	// through to the deadline instead.
	res := RunStop(context.Background(), time.Second, "*postgres.DB", panicDuringStop)

	if res.Status != StatusFailed {
		t.Fatalf("Status = %v, want failed", res.Status)
	}
	if res.Name != "*postgres.DB" {
		t.Fatalf("Name = %q, want the node's own name", res.Name)
	}
	if res.Err == nil {
		t.Fatal("Err = nil; a failed node carrying no error tells the operator nothing about why")
	}

	msg := res.Err.Error()
	for _, want := range []struct{ substring, why string }{
		{"*postgres.DB", "the operator has to know which node it was"},
		{"panicked during stop", "a panic is not an ordinary Close error and must not read like one"},
		{"close of closed database", "the panic value is the only description of the fault"},
		{"panicDuringStop", "the captured stack must point at the frame that panicked, not at servo's recover"},
	} {
		if !strings.Contains(msg, want.substring) {
			t.Errorf("Err = %q, want it to mention %q: %s", msg, want.substring, want.why)
		}
	}
}

// TestRunStopKeepsWorkingAfterRecoveringAPanic is the payoff the recover
// was written for: teardown continues past the node that blew up. Note how
// it fails if the recover is removed — not with a diff, but by killing the
// test binary and taking every other test in this package with it, which
// is exactly what it would do to a production shutdown.
//
// The follow-up calls also pin that the recovered send did not corrupt the
// normal path. Both the recover and the ordinary return write to the same
// capacity-one channel, so a version that ever sent twice would leave a
// goroutine parked on the second send; the nodes stopped after the
// panicking one must still get their own honest results.
func TestRunStopKeepsWorkingAfterRecoveringAPanic(t *testing.T) {
	if res := RunStop(context.Background(), time.Second, "*postgres.DB", panicDuringStop); res.Status != StatusFailed {
		t.Fatalf("panicking node: got %+v, want failed", res)
	}

	ok := RunStop(context.Background(), time.Second, "*logger.Logger", func(context.Context) error {
		return nil
	})
	if ok.Status != StatusOK || ok.Err != nil {
		t.Fatalf("node stopped after the panic: got %+v, want a clean ok", ok)
	}

	want := errors.New("flush failed")
	failed := RunStop(context.Background(), time.Second, "*metrics.Sink", func(context.Context) error {
		return want
	})
	if failed.Status != StatusFailed || !errors.Is(failed.Err, want) {
		t.Fatalf("node stopped after the panic: got %+v, want failed wrapping %v", failed, want)
	}
}

// TestRunStopDoesNotLeakWhenAnAbandonedNodeLaterPanics covers the one
// arrangement in which a second send really would deadlock. RunStop has
// already given up and returned, so nothing is reading the done channel
// and its single buffer slot is the only place a send can go. A goroutine
// that both returned a value and recovered a panic would park on the
// second send for the life of the process — the leak the buffered channel
// was introduced to avoid, reappearing on the path nobody watches.
func TestRunStopDoesNotLeakWhenAnAbandonedNodeLaterPanics(t *testing.T) {
	// Baseline rather than a bare VerifyNone: a sibling test in this file
	// parks a goroutine on purpose, and inheriting its leak here would
	// report a fault that is not this test's.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	proceed := make(chan struct{})
	res := RunStop(context.Background(), 5*time.Millisecond, "slow", func(context.Context) error {
		<-proceed // outlives the budget, then dies badly
		panic("close of closed database")
	})
	if res.Status != StatusAbandoned {
		t.Fatalf("got %+v, want abandoned", res)
	}

	// Only now does the abandoned goroutine panic, with RunStop long gone.
	// goleak retries, so it waits for the unwind rather than racing it.
	close(proceed)
}
