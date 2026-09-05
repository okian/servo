package servo

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// DefaultStopBudget bounds every node's init-rollback and shutdown calls
// when no other budget is given. It is a package var, not a config file,
// because configuration parsing is out of scope for this engine;
// servotest.Timeout overrides it for tests that exercise the abandoned-node
// path.
var DefaultStopBudget = 5 * time.Second

// RunStop runs fn in its own goroutine under budget, and reports the node
// abandoned rather than blocking forever if fn does not return in time. The
// result channel is buffered so a goroutine that outlives its budget can
// still send without leaking.
//
// A panic in fn is recovered and reported as StatusFailed, with the panic
// value and the stack that produced it. It has to be: the goroutine is
// servo's, not the caller's, so a recover in main cannot reach a panic
// here — the process would die mid-teardown with every node behind this
// one still running and no Report to say which. Turning it into one failed
// node lets the rest of the unwind finish and names the culprit.
func RunStop(ctx context.Context, budget time.Duration, name string, fn func(context.Context) error) NodeResult {
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// Only ever one send: the deferred recover reaches this line only
		// when fn panicked, which is precisely when the send below it did
		// not happen.
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("servo: %s panicked during stop: %v\n%s", name, r, debug.Stack())
			}
		}()
		done <- fn(cctx)
	}()

	select {
	case err := <-done:
		if err != nil {
			return NodeResult{Name: name, Status: StatusFailed, Err: err}
		}
		return NodeResult{Name: name, Status: StatusOK}
	case <-cctx.Done():
		return NodeResult{Name: name, Status: StatusAbandoned, Err: cctx.Err()}
	}
}
