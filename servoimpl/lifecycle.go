package servoimpl

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/okian/servo/v3/servo"
)

// Call is one lifecycle method of one node: an entry in a level Init
// runs, a runner Run supervises, or the one call Guard makes.
type Call struct {
	// Node is the node's type string, the name a servo.NodeError
	// reports — or, for an emitted HTTP server, the name its stop result
	// and its Ready entry use ("http", "http:<group>").
	Node string
	// Fn is the method itself, as a method value such as a.db.Init.
	Fn func(context.Context) error
}

// Init runs the Init phase. Levels run in the order given, and the calls
// within a level concurrently, each on a goroutine of its own whatever the
// level's size: a lone Init and a shared one now get the same context, the
// same panic handling and the same error, where the generated code used to
// call a lone Init directly and give a shared level an errgroup — so how a
// component behaved depended on whether an unrelated Initializer happened
// to sit at its level.
//
// Every call gets a context of its own, derived from ctx and cancelled the
// moment that call returns: Init's context is for the call, and work that
// should outlive it belongs in Run. It is cancelled earlier when another
// call at the same level fails, when ctx itself ends, and, when budget is
// above zero, once budget has passed. A positive budget also bounds the
// wait: a call still running when it expires is abandoned — reported, and
// left running — rather than holding New open. Without one, a level waits
// for every call it started.
//
// A panic is recovered and becomes that call's error, with the panic value
// and the stack it was raised on. Every failure is a *servo.NodeError for
// the "Init" phase; the first one cancels its siblings, and errors they
// return only because of that cancellation are not reported again. When
// ctx ended before the phase finished — the shutdown signal arriving
// mid-startup — Init fails even if every call succeeded, with an error
// wrapping context.Cause(ctx): New must not hand back an application whose
// caller has already asked it to stop.
//
// report, when it is not nil, gets one entry per call in the order the
// calls finished, which is the order App.Report has always documented.
// Init returns nil, or the phase's failures — one error, or several
// joined.
func Init(ctx context.Context, budget time.Duration, report *servo.StartupReport, levels ...[]Call) error {
	for _, level := range levels {
		// Checked before a level starts, so a cancellation that arrived
		// during the previous one does not launch another round of
		// connects and migrations only to cancel them at once.
		if ctx.Err() != nil {
			return interrupted(ctx)
		}
		failures := initLevel(ctx, budget, report, level)
		if ctx.Err() != nil {
			return joinErrors(append([]error{interrupted(ctx)}, failures...))
		}
		if len(failures) > 0 {
			return joinErrors(failures)
		}
	}
	return nil
}

// outcome is one call's result, tagged with the call it belongs to.
type outcome struct {
	call    int
	err     error
	elapsed time.Duration
}

// initLevel runs one level's calls concurrently and returns their
// failures, each a *servo.NodeError, in the order they happened.
func initLevel(ctx context.Context, budget time.Duration, report *servo.StartupReport, level []Call) []error {
	levelCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	results := make(chan outcome, len(level))
	for i, c := range level {
		go func() { results <- initCall(levelCtx, budget, i, c) }()
	}

	var failures []error
	for range level {
		o := <-results
		if report != nil {
			report.Nodes = append(report.Nodes, servo.StartupNode{Type: level[o.call].Node, Duration: o.elapsed})
		}
		if o.err == nil || echoes(levelCtx, o.err) {
			continue
		}
		failure := &servo.NodeError{Node: level[o.call].Node, Phase: "Init", Err: o.err}
		failures = append(failures, failure)
		// Only the first cause is kept; later calls are no-ops.
		cancel(failure)
	}
	return failures
}

// initCall makes one Init call on a context of its own, and waits for it
// as long as the budget allows. The budget's timer is kept apart from the
// context's own Done channel on purpose: that channel also closes when a
// sibling fails or New's context ends, and neither of those is a reason to
// stop waiting for a call that may be about to return — only the budget
// running out is.
func initCall(levelCtx context.Context, budget time.Duration, i int, c Call) outcome {
	start := time.Now()
	callCtx, cancel := context.WithCancel(levelCtx)
	if budget > 0 {
		callCtx, cancel = context.WithTimeout(levelCtx, budget)
	}
	defer cancel()

	done := make(chan error, 1)
	spawn(callCtx, c.Fn, func(err error) { done <- err })

	var err error
	abandoned := false
	if budget > 0 {
		timer := time.NewTimer(budget)
		select {
		case err = <-done:
		case <-timer.C:
			abandoned = true
		}
		timer.Stop()
	} else {
		err = <-done
	}

	switch {
	case abandoned:
		err = fmt.Errorf("did not return within its %v budget (servo.DefaultInitBudget), so New stopped waiting for it and left it running: %w", budget, context.DeadlineExceeded)
	case err != nil && budget > 0 && errors.Is(callCtx.Err(), context.DeadlineExceeded) && levelCtx.Err() == nil:
		// It came back with an error once its own budget had run out,
		// and nothing else had cancelled it: say which limit it ran
		// into, since its own error rarely does.
		err = fmt.Errorf("exceeded its %v budget (servo.DefaultInitBudget): %w", budget, err)
	}
	return outcome{call: i, err: err, elapsed: time.Since(start)}
}

// Run supervises the generated Run's runners: every call on a goroutine
// of its own, on one shared context derived from ctx. It returns once
// every runner has returned, or once ctx is done and grace has passed,
// whichever comes first.
//
// A runner that fails — returns an error that is not merely ctx's own
// ending, or panics — cancels the rest, which then have grace to return.
// A runner that returns nil cancels nothing: a graph may well hold a
// runner with a finite job beside a server meant to run until the signal.
// Once ctx is done, for either reason, a runner still running after grace
// is left running and reported: Run's caller is about to call Shutdown,
// and a component that never noticed it was asked to stop must not keep
// the whole process — listeners included — serving until the supervisor
// kills it.
//
// The result, in order of preference: every failure, each a
// *servo.NodeError for the "Run" phase, a panic or a runner left running
// included — one error, or several joined, so a failure is never masked
// by a sibling that merely returned its cancellation first; otherwise the
// first error a runner returned because ctx ended, unchanged, which is
// what Run has always returned on an ordinary shutdown; otherwise nil.
func Run(ctx context.Context, grace time.Duration, calls ...Call) error {
	if len(calls) == 0 {
		return nil
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	results := make(chan outcome, len(calls))
	for i, c := range calls {
		spawn(runCtx, c.Fn, func(err error) { results <- outcome{call: i, err: err} })
	}

	returned := make([]bool, len(calls))
	pending := len(calls)
	var failures []error
	var echo error
	ended := runCtx.Done()
	var timer *time.Timer
	var graceUp <-chan time.Time

	for pending > 0 {
		select {
		case o := <-results:
			pending--
			returned[o.call] = true
			switch {
			case o.err == nil:
			case echoes(runCtx, o.err):
				if echo == nil {
					echo = o.err
				}
			default:
				failure := &servo.NodeError{Node: calls[o.call].Node, Phase: "Run", Err: o.err}
				failures = append(failures, failure)
				cancel(failure)
			}
		case <-ended:
			// Armed once: from here every runner still out has grace
			// left to return.
			ended = nil
			timer = time.NewTimer(max(grace, 0))
			graceUp = timer.C
		case <-graceUp:
			for i, ok := range returned {
				if !ok {
					failures = append(failures, &servo.NodeError{Node: calls[i].Node, Phase: "Run", Err: fmt.Errorf("still running %v after its context ended (servo.DefaultRunGrace), so Run stopped waiting for it and left it running", grace)})
				}
			}
			pending = 0
		}
	}
	if timer != nil {
		timer.Stop()
	}

	if len(failures) > 0 {
		return joinErrors(failures)
	}
	return echo
}

// Guard makes one call on the caller's goroutine and returns its error as
// a *servo.NodeError for phase, a panic included. Generated code runs a
// scoped instance's Run through it: that goroutine is servo's, so nothing
// any caller wrote could recover a panic there, and unrecovered it would
// take every other key's instance down with this one.
func Guard(ctx context.Context, phase string, c Call) error {
	if err := call(ctx, c.Fn); err != nil {
		return &servo.NodeError{Node: c.Node, Phase: phase, Err: err}
	}
	return nil
}

// RolledBack is what New returns once a failed startup has been unwound by
// the App's own Shutdown: err itself when the unwind came out clean, and
// otherwise err carrying the unwind's report.
//
// The report is reachable, and deliberately not traversable. errors.As
// with a *servo.Report target retrieves it, and the message ends with it,
// but errors.Is and errors.As do not descend into its nodes' errors. The
// unwind used to be joined to err, and a rollback Stop that overran its
// budget then made errors.Is(err, context.DeadlineExceeded) true — so a
// caller asking whether startup timed out was told yes because of a node
// that had nothing to do with why it failed.
func RolledBack(err error, rollback servo.Report) error {
	if err == nil || rollback.Clean() {
		return err
	}
	return &rolledBack{err: err, rollback: rollback}
}

type rolledBack struct {
	err      error
	rollback servo.Report
}

func (e *rolledBack) Error() string {
	return e.err.Error() + "\nrollback: " + e.rollback.Error()
}

func (e *rolledBack) Unwrap() error { return e.err }

// As hands out the rollback report to errors.As without making its nodes'
// errors part of the chain.
func (e *rolledBack) As(target any) bool {
	if r, ok := target.(*servo.Report); ok {
		*r = e.rollback
		return true
	}
	return false
}

// spawn makes one call on a goroutine of its own and reports its outcome
// exactly once: its error, a panic as an error, or errGoexit for a call
// that ended its goroutine with runtime.Goexit — the path t.FailNow takes
// inside a test double — since otherwise nothing would ever be reported
// and the wait for it would never end. report must not block; every
// caller hands it a send on a buffered channel.
func spawn(ctx context.Context, fn func(context.Context) error, report func(error)) {
	go func() {
		returned := false
		defer func() {
			if !returned {
				report(errGoexit)
			}
		}()
		err := call(ctx, fn)
		returned = true
		report(err)
	}()
}

// call makes one call and returns its error, with a panic turned into a
// *panicError carrying the value and the stack it was raised on.
func call(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{value: r, stack: debug.Stack()}
		}
	}()
	return fn(ctx)
}

// panicError is a recovered panic as an error. The value is formatted,
// never wrapped: a panic whose value happens to be context.Canceled must
// not pass for the cancellation a runner returns when it is asked to stop,
// and be dropped as one.
type panicError struct {
	value any
	stack []byte
}

func (p *panicError) Error() string {
	return fmt.Sprintf("panicked: %v\n%s", p.value, p.stack)
}

var errGoexit = errors.New("ended its goroutine with runtime.Goexit instead of returning")

// echoes reports whether err is ctx's own ending handed back: what a call
// returns because the context it was given was cancelled, by its parent or
// by a sibling's failure. Such an error says why the call stopped, not
// that anything went wrong in it.
func echoes(ctx context.Context, err error) bool {
	if ctx.Err() == nil {
		return false
	}
	return errors.Is(err, ctx.Err()) || errors.Is(err, context.Cause(ctx))
}

// interrupted is the error Init returns when New's own context ended
// before the phase finished. errors.Is matches context.Canceled for a
// cancellation — signal.NotifyContext's cause, "interrupt signal
// received", says it is one — and context.DeadlineExceeded for a
// deadline.
func interrupted(ctx context.Context) error {
	err, cause := ctx.Err(), context.Cause(ctx)
	if errors.Is(cause, err) {
		return fmt.Errorf("servo: startup interrupted: %w", cause)
	}
	// A cause set through context.WithCancelCause need not say it is a
	// cancellation; carry the context's own error as well.
	return fmt.Errorf("servo: startup interrupted: %w (%w)", cause, err)
}

// joinErrors is errors.Join, except that a single error comes back as
// itself, so the common case is a *servo.NodeError a type assertion can
// see, not a join wrapping one.
func joinErrors(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}
