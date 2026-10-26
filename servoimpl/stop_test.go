package servoimpl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/okian/servo/v3/servo"
)

// panicDuringStop stands in for the shutdown method that blows up in
// practice. Named, so the recovered stack can be asserted to point at it.
func panicDuringStop(context.Context) error {
	panic("close of closed database")
}

func TestRunStopOutcomes(t *testing.T) {
	defer goleak.VerifyNone(t)

	if res := RunStop(context.Background(), time.Second, "x", ok); res.Status != servo.StatusOK || res.Err != nil || res.Name != "x" {
		t.Fatalf("got %+v, want a clean ok", res)
	}

	boom := errors.New("boom")
	if res := RunStop(context.Background(), time.Second, "x", func(context.Context) error { return boom }); res.Status != servo.StatusFailed || !errors.Is(res.Err, boom) {
		t.Fatalf("got %+v, want failed with the error", res)
	}

	res := RunStop(context.Background(), time.Second, "*postgres.DB", panicDuringStop)
	if res.Status != servo.StatusFailed {
		t.Fatalf("got %+v, want a recovered panic reported as failed", res)
	}
	for _, want := range []string{"*postgres.DB panicked during stop", "close of closed database", "panicDuringStop"} {
		if !strings.Contains(res.Err.Error(), want) {
			t.Errorf("Err = %q, want it to mention %q", res.Err, want)
		}
	}

	release := make(chan struct{})
	res = RunStop(context.Background(), 10*time.Millisecond, "slow", func(context.Context) error {
		<-release
		return nil
	})
	close(release)
	if res.Status != servo.StatusAbandoned || !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("got %+v, want abandoned at the budget", res)
	}
}

// TestRunStopDoesNotStartWhatItCannotWaitFor covers a shutdown that has
// already overrun its deadline when the next node's turn comes. Starting
// the stop anyway started it in the background, beside every other node
// still to come, with nothing waiting for any of them — the database
// closed while the producer above it was still flushing into it. A stop
// that never began is reported abandoned, says it was not attempted, and
// still answers errors.Is with the reason the context gave.
func TestRunStopDoesNotStartWhatItCannotWaitFor(t *testing.T) {
	defer goleak.VerifyNone(t)

	for _, tc := range []struct {
		name   string
		ctx    func() (context.Context, context.CancelFunc)
		budget time.Duration
		want   error
	}{
		{
			name: "deadline passed",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			budget: time.Second,
			want:   context.DeadlineExceeded,
		},
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			budget: time.Second,
			want:   context.Canceled,
		},
		{
			// A budget already spent refuses the call for the same reason:
			// it could only ever be started and walked away from.
			name:   "no budget",
			ctx:    func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			budget: 0,
			want:   context.DeadlineExceeded,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()

			called := make(chan struct{})
			res := RunStop(ctx, tc.budget, "*postgres.DB", func(context.Context) error {
				close(called)
				return nil
			})
			// RunStop reports abandoned the same instant either way, so the
			// only witness to a stop started in the background is the
			// background: give it every chance to show up.
			select {
			case <-called:
				t.Fatal("the stop ran although its context was done before it could start")
			case <-time.After(50 * time.Millisecond):
			}
			if res.Status != servo.StatusAbandoned || !errors.Is(res.Err, tc.want) {
				t.Fatalf("got %+v, want abandoned wrapping %v", res, tc.want)
			}
			if !strings.Contains(res.Err.Error(), "not attempted") {
				t.Fatalf("Err = %q, want it to say the stop was not attempted", res.Err)
			}
		})
	}
}

func TestMergeNodeResults(t *testing.T) {
	errA, errB := errors.New("a failed"), errors.New("b abandoned")

	if m := MergeNodeResults("x", servo.NodeResult{Status: servo.StatusOK}, servo.NodeResult{Status: servo.StatusOK}); m.Status != servo.StatusOK || m.Err != nil || m.Name != "x" {
		t.Fatalf("got %+v, want a clean ok named x", m)
	}
	if m := MergeNodeResults("x", servo.NodeResult{Status: servo.StatusOK}, servo.NodeResult{Status: servo.StatusFailed, Err: errA}); m.Status != servo.StatusFailed || !errors.Is(m.Err, errA) {
		t.Fatalf("got %+v, want failed to outrank ok", m)
	}
	m := MergeNodeResults("x",
		servo.NodeResult{Status: servo.StatusAbandoned, Err: errB},
		servo.NodeResult{Status: servo.StatusFailed, Err: errA},
	)
	if m.Status != servo.StatusAbandoned || !errors.Is(m.Err, errA) || !errors.Is(m.Err, errB) {
		t.Fatalf("got %+v, want abandoned to outrank failed, whichever came first, with both errors joined", m)
	}
}

// TestLingerWindow is not parallel: it writes the package variable every
// generated scope reads.
func TestLingerWindow(t *testing.T) {
	prev := servo.LingerOverride
	t.Cleanup(func() { servo.LingerOverride = prev })

	servo.LingerOverride = -1
	if got := LingerWindow(30 * time.Second); got != 30*time.Second {
		t.Fatalf("LingerWindow = %s, want the declared window", got)
	}
	// Zero is a real policy, not "unset".
	servo.LingerOverride = 0
	if got := LingerWindow(30 * time.Second); got != 0 {
		t.Fatalf("LingerWindow = %s, want 0", got)
	}
	servo.LingerOverride = 5 * time.Millisecond
	if got := LingerWindow(30 * time.Second); got != 5*time.Millisecond {
		t.Fatalf("LingerWindow = %s, want the override", got)
	}
}
