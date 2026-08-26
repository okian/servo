package servo

import (
	"context"
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
func RunStop(ctx context.Context, budget time.Duration, name string, fn func(context.Context) error) NodeResult {
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- fn(cctx) }()

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
