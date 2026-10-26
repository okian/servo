package servo

import (
	"context"
	"errors"
)

// ExitCode is the status a process should exit with once Run has returned
// and Shutdown has reported:
//
//   - 0 when Run returned nil, or nothing but the cancellation of its
//     context, and every node stopped cleanly;
//   - 1 otherwise: a runner failed, panicked or would not stop, or a node
//     failed or was abandoned during Shutdown.
//
// Main exits with it. A main written by hand ends with it:
//
//	os.Exit(servo.ExitCode(err, report))
//
// It exists because the obvious main logs a failure and falls off the end,
// and a process that returns from main exits 0: a supervisor told to
// restart on failure never restarts it, and a batch job that failed
// reports success. 1 is what log.Fatal exits with, so a main that fails in
// New and one that fails in Run agree.
//
// A cancellation counts as clean because Run's context is the signal
// context, and a runner returning ctx.Err() once it is cancelled is the
// ordinary way to stop. Only a cancellation does. An error joining one
// with anything else is a failure, and so is context.DeadlineExceeded,
// since a deadline is not a shutdown signal. So is a *NodeError, whatever
// it wraps: the generated Run wraps only failures in one, and returns the
// cancellation a runner hands back on an ordinary stop unchanged.
func ExitCode(runErr error, r Report) int {
	if runErr != nil && !canceledOnly(runErr) {
		return 1
	}
	if !r.Clean() {
		return 1
	}
	return 0
}

// canceledOnly reports whether err is context.Canceled and nothing else:
// the error itself, a chain wrapping it, or a join every branch of which
// is. errors.Is answers a weaker question — whether a cancellation is in
// there anywhere — which a runner's real failure joined with the
// cancellation that followed it would pass.
func canceledOnly(err error) bool {
	switch e := err.(type) {
	case *NodeError:
		return false
	case interface{ Unwrap() []error }:
		errs := e.Unwrap()
		for _, inner := range errs {
			if !canceledOnly(inner) {
				return false
			}
		}
		return len(errs) > 0
	case interface{ Unwrap() error }:
		if inner := e.Unwrap(); inner != nil {
			return canceledOnly(inner)
		}
	}
	return errors.Is(err, context.Canceled)
}
