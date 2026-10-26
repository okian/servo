package servoimpl

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/okian/servo/v3/servo"
)

// RunStop runs fn in its own goroutine under budget, and reports the node
// abandoned rather than blocking forever if fn does not return in time.
// Generated code makes every Drain, Flush, Stop and cleanup call through
// it, during Shutdown and during both rollbacks. The result channel is
// buffered so a goroutine that outlives its budget can still send without
// leaking.
//
// When ctx is already done, fn is not called at all: the node is reported
// abandoned with an error saying it was not attempted, wrapping ctx's own.
// Starting it anyway would start it in the background — the select below
// gives up on it the same instant — so once a shutdown's deadline had
// passed, every node still to come began stopping at once, concurrently
// and out of the order the graph requires, while main exited around them:
// a database closed under a producer that was still flushing into it.
//
// A panic in fn is recovered and reported as StatusFailed, with the panic
// value and the stack that produced it. The goroutine is servo's, not the
// caller's, so no recover in main could reach a panic here: unrecovered,
// the process would die mid-teardown with every node behind this one still
// running and no Report to say which.
func RunStop(ctx context.Context, budget time.Duration, name string, fn func(context.Context) error) servo.NodeResult {
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// cctx rather than ctx, so a budget that is already spent — zero or
	// negative — refuses the call for the same reason.
	if err := cctx.Err(); err != nil {
		return servo.NodeResult{Name: name, Status: servo.StatusAbandoned, Err: fmt.Errorf("servo: not attempted, its context was already done: %w", err)}
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
			return servo.NodeResult{Name: name, Status: servo.StatusFailed, Err: err}
		}
		return servo.NodeResult{Name: name, Status: servo.StatusOK}
	case <-cctx.Done():
		return servo.NodeResult{Name: name, Status: servo.StatusAbandoned, Err: cctx.Err()}
	}
}

// MergeNodeResults combines a node's per-phase results (Drain, Flush,
// Stop, cleanup) into the single per-node outcome a servo.Report
// enumerates: abandoned outranks failed outranks ok, and every phase error
// is joined.
func MergeNodeResults(name string, results ...servo.NodeResult) servo.NodeResult {
	merged := servo.NodeResult{Name: name, Status: servo.StatusOK}
	var errs []error
	for _, res := range results {
		switch {
		case res.Status == servo.StatusAbandoned:
			merged.Status = servo.StatusAbandoned
		case res.Status == servo.StatusFailed && merged.Status != servo.StatusAbandoned:
			merged.Status = servo.StatusFailed
		}
		if res.Err != nil {
			errs = append(errs, res.Err)
		}
	}
	if len(errs) > 0 {
		merged.Err = errors.Join(errs...)
	}
	return merged
}
