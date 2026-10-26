package servo_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
	"github.com/okian/servo/v3/servoimpl"
)

func TestExitCode(t *testing.T) {
	failure := errors.New("consumer: broker unreachable")
	clean := servo.Report{Nodes: []servo.NodeResult{{Name: "*api.Server", Status: servo.StatusOK}}}
	failed := servo.Report{Nodes: []servo.NodeResult{{Name: "*api.Server", Status: servo.StatusFailed, Err: failure}}}
	abandoned := servo.Report{Nodes: []servo.NodeResult{{Name: "*postgres.DB", Status: servo.StatusAbandoned, Err: context.DeadlineExceeded}}}

	for _, tc := range []struct {
		name   string
		runErr error
		report servo.Report
		want   int
	}{
		{"nothing to report", nil, servo.Report{}, 0},
		{"clean run, clean shutdown", nil, clean, 0},

		// The signal context's cancellation is how Run is asked to stop,
		// and a runner returning ctx.Err() is the ordinary answer.
		{"cancelled by the signal", context.Canceled, clean, 0},
		{"cancellation wrapped", fmt.Errorf("consumer: %w", context.Canceled), clean, 0},
		{"every branch a cancellation", errors.Join(context.Canceled, fmt.Errorf("worker: %w", context.Canceled)), clean, 0},

		{"runner failed", failure, clean, 1},
		{"runner failed, named by Run", &servo.NodeError{Node: "*worker.Consumer", Phase: "Run", Err: failure}, clean, 1},
		// The generated Run hands back a runner's cancellation unchanged,
		// so a NodeError is a failure even when it wraps one: a runner
		// that returned context.Canceled while nothing had cancelled it.
		{"a NodeError wrapping a cancellation", &servo.NodeError{Node: "*worker.Consumer", Phase: "Run", Err: context.Canceled}, clean, 1},
		{"a NodeError joined with a cancellation", errors.Join(context.Canceled, &servo.NodeError{Node: "*worker.Consumer", Phase: "Run", Err: context.Canceled}), clean, 1},
		// A deadline is not the shutdown signal: a Run that ran out of
		// time did not finish its work.
		{"deadline exceeded", context.DeadlineExceeded, clean, 1},
		// errors.Is would find the cancellation in here and call it clean,
		// hiding the failure joined beside it.
		{"failure joined with a cancellation", errors.Join(context.Canceled, failure), clean, 1},
		{"failure wrapping such a join", fmt.Errorf("run: %w", errors.Join(failure, context.Canceled)), clean, 1},

		{"node failed to stop", nil, failed, 1},
		{"node abandoned", nil, abandoned, 1},
		{"cancelled, but a node failed to stop", context.Canceled, failed, 1},
		{"both failed", failure, abandoned, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := servo.ExitCode(tc.runErr, tc.report); got != tc.want {
				t.Fatalf("ExitCode(%v, %v) = %d, want %d", tc.runErr, tc.report, got, tc.want)
			}
		})
	}
}

// TestExitCodeAgreesWithTheGeneratedRun pins ExitCode to what the
// generated Run actually returns, rather than to errors built by hand: a
// change in either that made an ordinary shutdown exit 1, or a failure
// exit 0, fails here.
func TestExitCodeAgreesWithTheGeneratedRun(t *testing.T) {
	waitThenEcho := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	waitThenWrap := func(ctx context.Context) error {
		<-ctx.Done()
		return fmt.Errorf("consumer: stopped: %w", ctx.Err())
	}
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })

	for _, tc := range []struct {
		name string
		// cancel is whether the test cancels Run's context once the
		// runners have started, as the signal would.
		cancel bool
		calls  []servoimpl.Call
		want   int
	}{
		{"no runners", false, nil, 0},
		{"a finite job that finished", false, []servoimpl.Call{
			{Node: "*migrate.Job", Fn: func(context.Context) error { return nil }},
		}, 0},
		{"stopped by the signal", true, []servoimpl.Call{
			{Node: "*api.Server", Fn: waitThenEcho},
			{Node: "*worker.Consumer", Fn: waitThenWrap},
		}, 0},
		{"a runner failed", false, []servoimpl.Call{
			{Node: "*api.Server", Fn: waitThenEcho},
			{Node: "*worker.Consumer", Fn: func(context.Context) error { return errors.New("broker unreachable") }},
		}, 1},
		{"a runner panicked", false, []servoimpl.Call{
			{Node: "*api.Server", Fn: waitThenEcho},
			{Node: "*worker.Consumer", Fn: func(context.Context) error { panic("nil map") }},
		}, 1},
		{"a runner returned a cancellation nobody asked for", false, []servoimpl.Call{
			{Node: "*api.Server", Fn: waitThenEcho},
			{Node: "*worker.Consumer", Fn: func(context.Context) error { return context.Canceled }},
		}, 1},
		{"a runner that would not stop", true, []servoimpl.Call{
			{Node: "*worker.Consumer", Fn: func(context.Context) error { <-stuck; return nil }},
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				// Late enough that every runner is already waiting on
				// ctx; the grace below is what bounds the stuck one.
				time.AfterFunc(10*time.Millisecond, cancel)
			}
			runErr := servoimpl.Run(ctx, 50*time.Millisecond, tc.calls...)
			if got := servo.ExitCode(runErr, servo.Report{}); got != tc.want {
				t.Fatalf("Run returned %v; ExitCode = %d, want %d", runErr, got, tc.want)
			}
		})
	}
}
