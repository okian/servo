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

// DefaultInitBudget bounds each node's Init call during New. Zero, the
// default, means no bound: New waits for every Init to return, as it
// always has.
//
// Set above zero, it becomes the deadline of the context each Init
// receives, and the limit on how long New waits for that call. An Init
// that respects its context returns when the budget runs out, and fails
// New naming the budget; one that ignores its context is abandoned at the
// same moment — New stops waiting for it, fails with that node's error,
// and rolls back while the call is left running, exactly as RunStop treats
// a Stop that overruns. It exists because a dependency that accepts a
// connection and never answers used to hold New open with no output and
// no error, for as long as the operator was willing to wait.
//
// Like DefaultStopBudget it is read at call time, so set it before New,
// and tests that change it must not run in parallel with other tests that
// call New.
var DefaultInitBudget time.Duration

// DefaultRunGrace is how long the generated Run keeps waiting for its
// runners once its context is done — because the shutdown signal arrived,
// or because another runner failed and cancelled the rest. A runner still
// running past it is left running: Run returns an error naming it, so
// main's Shutdown can begin instead of waiting on a component that never
// noticed it was asked to stop.
//
// It exists because Run used to wait for every runner without limit, and
// a runner ignoring its context held the whole process — listeners still
// serving, nothing drained — until the supervisor's grace period ran out
// and it was killed outright. The 5 second default matches
// DefaultStopBudget: one budget for the runner to wind down, then
// Shutdown's own budgets for everything else. A deployment sizing its
// termination grace period should count it in.
//
// Read at call time, like DefaultStopBudget; set it before Run.
var DefaultRunGrace = 5 * time.Second

// RunStop runs fn in its own goroutine under budget, and reports the node
// abandoned rather than blocking forever if fn does not return in time. The
// result channel is buffered so a goroutine that outlives its budget can
// still send without leaking.
//
// When ctx is already done, fn is not called at all: the node is reported
// abandoned with an error saying it was not attempted, wrapping ctx's own.
// Starting it anyway started it in the background — the select gave up on
// it the same instant — so once a shutdown's deadline had passed, every
// node still to come began stopping at once, concurrently and out of the
// order the graph requires, while main exited around them: a database
// closed under a producer that was still flushing into it.
//
// A panic in fn is recovered and reported as StatusFailed, with the panic
// value and the stack that produced it. It has to be: the goroutine is
// servo's, not the caller's, so a recover in main cannot reach a panic
// here — the process would die mid-teardown with every node behind this
// one still running and no Report to say which. Turning it into one failed
// node lets the rest of the unwind finish and names the culprit.
//
// Deprecated: generated code calls servoimpl.RunStop since v3.5.0; this
// remains for files generated earlier.
func RunStop(ctx context.Context, budget time.Duration, name string, fn func(context.Context) error) NodeResult {
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// cctx rather than ctx, so a budget that is already spent — zero or
	// negative — refuses the call for the same reason.
	if err := cctx.Err(); err != nil {
		return NodeResult{Name: name, Status: StatusAbandoned, Err: fmt.Errorf("servo: not attempted, its context was already done: %w", err)}
	}

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
