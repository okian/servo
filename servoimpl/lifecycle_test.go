package servoimpl

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/okian/servo/v3/servo"
)

// panicDuringInit and panicDuringRun are named functions rather than
// closures so the assertions on a recovered stack can name the frame that
// actually panicked: showing that frame is the only reason a stack is
// captured at all.
func panicDuringInit(context.Context) error {
	panic("assignment to entry in nil map")
}

func panicDuringRun(context.Context) error {
	panic("send on closed channel")
}

func ok(context.Context) error { return nil }

// returnsOnCancel blocks until its context ends and reports that.
func returnsOnCancel(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// waitFor fails the test if ch is not closed or sent on within d. Every
// test here that waits on a goroutine does it through this, so a defect
// shows up as a failure naming what never happened, not as a hung suite.
func waitFor[T any](t *testing.T, ch <-chan T, d time.Duration, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf("timed out after %v waiting for %s", d, what)
		var zero T
		return zero
	}
}

func asNodeError(t *testing.T, err error) *servo.NodeError {
	t.Helper()
	var ne *servo.NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("err = %v (%T), want a *servo.NodeError in its chain", err, err)
	}
	return ne
}

// TestInitContextIsCallScopedAtEveryLevel is the REPORT's §1.6 case. A
// lone Init used to be handed New's own, long-lived context and a shared
// level an errgroup's, which dies when Wait returns — so a refresher an
// Init started on its context lived on when the node was alone at its
// level and died the moment an unrelated Initializer joined it. Both get
// the same rule now: the context is cancelled once the call returns.
func TestInitContextIsCallScopedAtEveryLevel(t *testing.T) {
	defer goleak.VerifyNone(t)

	for _, size := range []int{1, 2} {
		t.Run(fmt.Sprintf("level of %d", size), func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()

			seen := make([]context.Context, size)
			level := make([]Call, size)
			for i := range level {
				level[i] = Call{Node: fmt.Sprintf("*app.N%d", i), Fn: func(ctx context.Context) error {
					seen[i] = ctx
					return nil
				}}
			}
			if err := Init(parent, 0, nil, level); err != nil {
				t.Fatalf("Init = %v", err)
			}
			for i, ctx := range seen {
				if ctx.Err() == nil {
					t.Errorf("call %d's context is still live after Init returned; work bound to it outlives the call", i)
				}
			}
			if parent.Err() != nil {
				t.Fatal("Init cancelled its caller's context")
			}
		})
	}
}

// TestInitPassesValuesThrough: a call's context is cut short, not cut
// off — whatever New's context carried reaches every Init.
func TestInitPassesValuesThrough(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	err := Init(ctx, 0, nil, []Call{{Node: "*app.DB", Fn: func(ctx context.Context) error {
		if ctx.Value(key{}) != "v" {
			return errors.New("the value New's context carried did not arrive")
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

// TestInitRunsLevelsInOrderAndConcurrentlyWithin: a level does not start
// until the one before it has finished, and the calls inside one run at
// the same time — each of these two blocks until the other has started,
// which only concurrent calls can satisfy.
func TestInitRunsLevelsInOrderAndConcurrentlyWithin(t *testing.T) {
	defer goleak.VerifyNone(t)

	aStarted, bStarted := make(chan struct{}), make(chan struct{})
	firstDone := make(chan struct{})
	var secondSawFirstDone bool
	err := Init(context.Background(), 0, nil,
		[]Call{
			{Node: "*app.A", Fn: func(context.Context) error {
				close(aStarted)
				waitFor(t, bStarted, 5*time.Second, "B to start alongside A")
				return nil
			}},
			{Node: "*app.B", Fn: func(context.Context) error {
				close(bStarted)
				waitFor(t, aStarted, 5*time.Second, "A to start alongside B")
				close(firstDone)
				return nil
			}},
		},
		[]Call{
			{Node: "*app.C", Fn: func(context.Context) error {
				select {
				case <-firstDone:
					secondSawFirstDone = true
				default:
				}
				return nil
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !secondSawFirstDone {
		t.Fatal("the second level started before the first had finished")
	}
}

// TestInitRecoversAPanic covers a panic on a lone level and on a shared
// one. The lone one used to unwind into main and the shared one killed the
// process from the errgroup's goroutine; neither ran the rollback. Now it
// is that node's Init failure, an error New can roll back from, carrying
// the value and the frame that raised it.
func TestInitRecoversAPanic(t *testing.T) {
	defer goleak.VerifyNone(t)

	for _, tc := range []struct {
		name  string
		level []Call
	}{
		{"alone", []Call{{Node: "*app.Flaky", Fn: panicDuringInit}}},
		{"beside a sibling", []Call{{Node: "*app.Flaky", Fn: panicDuringInit}, {Node: "*app.Other", Fn: returnsOnCancel}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Init(context.Background(), 0, nil, tc.level)
			if err == nil {
				t.Fatal("Init = nil; a panic must come back as an error")
			}
			ne := asNodeError(t, err)
			if ne.Node != "*app.Flaky" || ne.Phase != "Init" {
				t.Fatalf("NodeError = %q/%q, want *app.Flaky/Init", ne.Node, ne.Phase)
			}
			for _, want := range []string{"*app.Flaky: Init: panicked: assignment to entry in nil map", "panicDuringInit"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to contain %q", err, want)
				}
			}
		})
	}
}

// TestInitFirstFailureCancelsTheRest: the sibling learns of the failure
// through its context, and the cancellation it returns because of it is
// not reported as a second failure — the error names the node that
// actually failed, and only that one.
func TestInitFirstFailureCancelsTheRest(t *testing.T) {
	defer goleak.VerifyNone(t)

	refused := errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")
	secondLevelRan := false
	err := Init(context.Background(), 0, nil,
		[]Call{
			{Node: "*postgres.DB", Fn: func(context.Context) error { return refused }},
			{Node: "*redis.Cache", Fn: returnsOnCancel},
		},
		[]Call{{Node: "*api.Server", Fn: func(context.Context) error { secondLevelRan = true; return nil }}},
	)
	ne := asNodeError(t, err)
	if ne.Node != "*postgres.DB" || ne.Phase != "Init" || !errors.Is(err, refused) {
		t.Fatalf("err = %v, want *postgres.DB's own failure, wrapped", err)
	}
	if got, want := err.Error(), "*postgres.DB: Init: "+refused.Error(); got != want {
		t.Fatalf("err = %q, want %q: the cancelled sibling is not a second failure", got, want)
	}
	if secondLevelRan {
		t.Fatal("a level ran after the one before it failed")
	}
}

// TestInitReportsEveryFailure: two real failures at one level are both
// reported, each naming its node.
func TestInitReportsEveryFailure(t *testing.T) {
	both := make(chan struct{})
	ready := make(chan struct{}, 2)
	fail := func(msg string) func(context.Context) error {
		return func(context.Context) error {
			ready <- struct{}{}
			<-both
			return errors.New(msg)
		}
	}
	go func() {
		<-ready
		<-ready
		close(both)
	}()
	err := Init(context.Background(), 0, nil, []Call{{Node: "*a.A", Fn: fail("a broke")}, {Node: "*b.B", Fn: fail("b broke")}})
	for _, want := range []string{"*a.A: Init: a broke", "*b.B: Init: b broke"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
}

// TestInitFailsWhenItsContextEnds is the REPORT's §1.4 case: an Init that
// ignores its context finishes after the shutdown signal has arrived, and
// New used to return success — Run then bound the port of a process that
// had already been told to stop. Init now fails, wrapping the context's
// cause, and starts no further level.
func TestInitFailsWhenItsContextEnds(t *testing.T) {
	defer goleak.VerifyNone(t)

	t.Run("during a level", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		secondLevelRan := false
		err := Init(ctx, 0, nil,
			[]Call{{Node: "*app.Slow", Fn: func(context.Context) error {
				cancel() // the signal arrives mid-Init, and this Init ignores it
				return nil
			}}},
			[]Call{{Node: "*app.Next", Fn: func(context.Context) error { secondLevelRan = true; return nil }}},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want it to wrap context.Canceled", err)
		}
		if !strings.Contains(err.Error(), "startup interrupted") {
			t.Fatalf("err = %q, want it to say startup was interrupted", err)
		}
		if secondLevelRan {
			t.Fatal("a level started after New's context had ended")
		}
	})

	t.Run("before it starts", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		err := Init(ctx, 0, nil, []Call{{Node: "*app.DB", Fn: func(context.Context) error { called = true; return nil }}})
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("err = %v, called = %v; want a cancellation and no call made", err, called)
		}
	})

	t.Run("the cause is kept", func(t *testing.T) {
		// signal.NotifyContext cancels with a cause naming the signal.
		// A cause that does not itself say it is a cancellation must
		// still match context.Canceled.
		sigterm := errors.New("terminated signal received")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(sigterm)
		err := Init(ctx, 0, nil, []Call{{Node: "*app.DB", Fn: ok}})
		if !errors.Is(err, sigterm) || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want it to match both the cause and context.Canceled", err)
		}
	})

	t.Run("with a failure beside it", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		broke := errors.New("migration 42 failed")
		err := Init(ctx, 0, nil, []Call{{Node: "*app.Migrator", Fn: func(context.Context) error {
			cancel()
			return broke
		}}})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, broke) {
			t.Fatalf("err = %v, want both the interruption and the node's own failure", err)
		}
	})
}

// TestInitBudget: a positive budget is the deadline of every call's
// context and the limit on waiting for it. A call that respects its
// context returns at the deadline and says which budget it ran into; one
// that ignores it is abandoned at the same moment instead of holding New.
func TestInitBudget(t *testing.T) {
	const budget = 50 * time.Millisecond

	t.Run("respected", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		err := Init(context.Background(), budget, nil, []Call{{Node: "*postgres.DB", Fn: returnsOnCancel}})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "servo.DefaultInitBudget") {
			t.Fatalf("err = %v, want a deadline naming the budget", err)
		}
		if ne := asNodeError(t, err); ne.Node != "*postgres.DB" {
			t.Fatalf("Node = %q", ne.Node)
		}
	})

	t.Run("ignored", func(t *testing.T) {
		release := make(chan struct{})
		defer goleak.VerifyNone(t)
		defer close(release)

		start := time.Now()
		err := Init(context.Background(), budget, nil, []Call{{Node: "*postgres.DB", Fn: func(context.Context) error {
			<-release // a black-holed connection that never looks at ctx
			return nil
		}}})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("Init took %v; the budget was %v", elapsed, budget)
		}
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not return within") {
			t.Fatalf("err = %v, want it abandoned at the budget", err)
		}
	})

	t.Run("a sibling's failure is not the budget", func(t *testing.T) {
		defer goleak.VerifyNone(t)
		broke := errors.New("broke")
		err := Init(context.Background(), time.Minute, nil, []Call{
			{Node: "*a.A", Fn: func(context.Context) error { return broke }},
			{Node: "*b.B", Fn: returnsOnCancel},
		})
		if strings.Contains(err.Error(), "budget") || !errors.Is(err, broke) {
			t.Fatalf("err = %v, want only A's failure, with no budget blamed", err)
		}
	})
}

// TestInitRecordsTimingsInCompletionOrder: App.Report's documented order.
// A outlasts B by a wide margin rather than by a channel handoff: the two
// results race to be delivered after their calls return, so only a gap
// between the returns themselves fixes which arrives first.
func TestInitRecordsTimingsInCompletionOrder(t *testing.T) {
	bReturned := make(chan struct{})
	var report servo.StartupReport
	err := Init(context.Background(), 0, &report,
		[]Call{
			{Node: "*app.A", Fn: func(context.Context) error {
				<-bReturned
				time.Sleep(100 * time.Millisecond)
				return nil
			}},
			{Node: "*app.B", Fn: func(context.Context) error { close(bReturned); return nil }},
		},
		[]Call{{Node: "*app.C", Fn: ok}},
	)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range report.Nodes {
		got = append(got, n.Type)
	}
	if strings.Join(got, ",") != "*app.B,*app.A,*app.C" {
		t.Fatalf("report = %v, want B (finished first), A, then the next level's C", got)
	}
}

// TestCallsSurviveGoexit: t.FailNow inside a test double ends its
// goroutine with runtime.Goexit, which neither returns nor panics. The
// wait for that call must still end.
func TestCallsSurviveGoexit(t *testing.T) {
	defer goleak.VerifyNone(t)

	goexit := func(context.Context) error {
		runtime.Goexit()
		return nil
	}
	if err := Init(context.Background(), 0, nil, []Call{{Node: "*app.Mock", Fn: goexit}}); err == nil || !strings.Contains(err.Error(), "Goexit") {
		t.Fatalf("Init = %v, want an error naming Goexit", err)
	}
	if err := Run(context.Background(), time.Second, Call{Node: "*app.Mock", Fn: goexit}); err == nil || !strings.Contains(err.Error(), "Goexit") {
		t.Fatalf("Run = %v, want an error naming Goexit", err)
	}
}

func TestRunReturnsNilWhenEveryRunnerDoes(t *testing.T) {
	defer goleak.VerifyNone(t)
	if err := Run(context.Background(), time.Second, Call{Node: "*a.A", Fn: ok}, Call{Node: "*b.B", Fn: ok}); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestRunAPanicIsOneFailedRunner is the R1 item: a runner's panic used to
// kill the process from the errgroup's goroutine, with Shutdown never run
// and a Flusher's buffer lost. It cancels the other runners like any
// failure, and Run returns it.
func TestRunAPanicIsOneFailedRunner(t *testing.T) {
	defer goleak.VerifyNone(t)

	err := Run(context.Background(), 5*time.Second,
		Call{Node: "*worker.Producer", Fn: panicDuringRun},
		Call{Node: "*api.Server", Fn: returnsOnCancel},
	)
	ne := asNodeError(t, err)
	if ne.Node != "*worker.Producer" || ne.Phase != "Run" {
		t.Fatalf("NodeError = %q/%q, want *worker.Producer/Run", ne.Node, ne.Phase)
	}
	for _, want := range []string{"panicked: send on closed channel", "panicDuringRun"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "*api.Server") {
		t.Errorf("err = %q: the server stopped because it was cancelled, which is not a second failure", err)
	}
}

// TestRunAPanicCarryingACancellationIsNotOne: a panic's value is
// formatted, never wrapped, so it cannot be filtered out as the
// cancellation a runner returns on an ordinary shutdown.
func TestRunAPanicCarryingACancellationIsNotOne(t *testing.T) {
	err := Guard(context.Background(), "Run", Call{Node: "*chat.Room", Fn: func(context.Context) error {
		panic(fmt.Errorf("room: %w", context.Canceled))
	}})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Guard = %v; a panic must never read as a cancellation", err)
	}
}

// TestRunOrdinaryShutdown: the signal cancels Run's context, every runner
// returns, and Run returns what it always has — nil when they return nil,
// the cancellation itself when they return that.
func TestRunOrdinaryShutdown(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, time.Second, Call{Node: "*a.A", Fn: returnsOnCancel}, Call{Node: "*b.B", Fn: returnsOnCancel})
	if err != context.Canceled {
		t.Fatalf("Run = %v (%T), want context.Canceled itself", err, err)
	}

	quiet := func(ctx context.Context) error { <-ctx.Done(); return nil }
	if err := Run(ctx, time.Second, Call{Node: "*a.A", Fn: quiet}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestRunAFailureIsNotMaskedByACancellation: the errgroup returned the
// first error, so a runner whose final flush failed after the signal was
// hidden behind a sibling that merely returned its cancellation first —
// and the process exited 0 with the data gone.
func TestRunAFailureIsNotMaskedByACancellation(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	flushFailed := errors.New("flush: broker unreachable")
	err := Run(ctx, 5*time.Second,
		Call{Node: "*api.Server", Fn: returnsOnCancel},
		Call{Node: "*worker.Producer", Fn: func(ctx context.Context) error {
			<-ctx.Done()
			time.Sleep(20 * time.Millisecond) // the final flush, after the server has gone
			return flushFailed
		}},
	)
	if ne := asNodeError(t, err); ne.Node != "*worker.Producer" || !errors.Is(err, flushFailed) {
		t.Fatalf("Run = %v, want the producer's failure", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v; the server's cancellation is not part of the failure", err)
	}
}

// TestRunANilReturnCancelsNothing: a runner with a finite job finishing
// is not a reason to stop the server beside it.
func TestRunANilReturnCancelsNothing(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	stillLive := make(chan bool, 1)
	jobDone := make(chan struct{})
	err := Run(ctx, time.Second,
		Call{Node: "*job.Once", Fn: func(context.Context) error { close(jobDone); return nil }},
		Call{Node: "*api.Server", Fn: func(runCtx context.Context) error {
			<-jobDone
			time.Sleep(10 * time.Millisecond)
			stillLive <- runCtx.Err() == nil
			cancel()
			<-runCtx.Done()
			return nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !<-stillLive {
		t.Fatal("a runner returning nil cancelled the others")
	}
}

// TestRunStopsWaitingAfterTheGrace is the REPORT's §1.4 case: a runner
// that ignores its context held Run — and the listeners — open after the
// signal until the supervisor killed the process. Run now returns after
// the grace, naming the runner it left behind, so Shutdown can begin.
func TestRunStopsWaitingAfterTheGrace(t *testing.T) {
	release := make(chan struct{})
	defer goleak.VerifyNone(t)
	defer close(release)

	stubborn := func(context.Context) error { <-release; return nil }
	const grace = 50 * time.Millisecond

	t.Run("after the signal", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		err := Run(ctx, grace, Call{Node: "*api.Server", Fn: returnsOnCancel}, Call{Node: "*worker.Stubborn", Fn: stubborn})
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("Run took %v; the grace was %v", elapsed, grace)
		}
		ne := asNodeError(t, err)
		if ne.Node != "*worker.Stubborn" || !strings.Contains(err.Error(), "still running") || !strings.Contains(err.Error(), "servo.DefaultRunGrace") {
			t.Fatalf("Run = %v, want the stubborn runner named as still running", err)
		}
		// Left running is a failure to stop, not an ordinary shutdown.
		if errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v must not read as a cancellation", err)
		}
	})

	t.Run("after a failure", func(t *testing.T) {
		broke := errors.New("consumer: broker unreachable")
		err := Run(context.Background(), grace,
			Call{Node: "*worker.Consumer", Fn: func(context.Context) error { return broke }},
			Call{Node: "*worker.Stubborn", Fn: stubborn},
		)
		if !errors.Is(err, broke) || !strings.Contains(err.Error(), "*worker.Stubborn: Run: still running") {
			t.Fatalf("Run = %v, want the failure and the runner left behind", err)
		}
	})

	t.Run("no grace", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := Run(ctx, 0, Call{Node: "*worker.Stubborn", Fn: stubborn})
		if !strings.Contains(fmt.Sprint(err), "still running") {
			t.Fatalf("Run = %v", err)
		}
	})
}

func TestGuard(t *testing.T) {
	if err := Guard(context.Background(), "Run", Call{Node: "*chat.Room", Fn: ok}); err != nil {
		t.Fatal(err)
	}

	gone := errors.New("subscriber gone")
	err := Guard(context.Background(), "Run", Call{Node: "*chat.Room", Fn: func(context.Context) error { return gone }})
	if ne := asNodeError(t, err); ne.Node != "*chat.Room" || ne.Phase != "Run" || !errors.Is(err, gone) {
		t.Fatalf("Guard = %v", err)
	}

	err = Guard(context.Background(), "Run", Call{Node: "*chat.Room", Fn: panicDuringRun})
	if ne := asNodeError(t, err); ne.Node != "*chat.Room" || !strings.Contains(err.Error(), "panicDuringRun") {
		t.Fatalf("Guard = %v, want the panic as the room's Run failure", err)
	}

	// The scoped Run launch filters a cancellation out with errors.Is; the
	// wrapper must not hide one from it.
	err = Guard(context.Background(), "Run", Call{Node: "*chat.Room", Fn: func(context.Context) error { return context.Canceled }})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Guard = %v, want errors.Is to see the cancellation", err)
	}
}

// TestRolledBack: the rollback report rides along without joining the
// chain errors.Is walks. Joined, a rollback Stop that overran its budget
// made errors.Is(err, context.DeadlineExceeded) true for a startup that
// failed for an unrelated reason, and named only the innocent node.
func TestRolledBack(t *testing.T) {
	failure := &servo.NodeError{Node: "*redis.Cache", Phase: "Init", Err: errors.New("connection refused")}

	if got := RolledBack(failure, servo.Report{Nodes: []servo.NodeResult{{Name: "*a.A", Status: servo.StatusOK}}}); got != failure {
		t.Fatalf("RolledBack after a clean unwind = %v, want the failure itself", got)
	}
	if got := RolledBack(failure, servo.Report{}); got != failure {
		t.Fatalf("RolledBack after an empty unwind = %v, want the failure itself", got)
	}

	unclean := servo.Report{Nodes: []servo.NodeResult{
		{Name: "*postgres.DB", Status: servo.StatusAbandoned, Err: context.DeadlineExceeded},
	}}
	err := RolledBack(failure, unclean)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("the rollback's abandoned stop leaked into errors.Is")
	}
	if !errors.Is(err, failure) {
		t.Fatal("errors.Is no longer reaches the startup failure")
	}
	if ne := asNodeError(t, err); ne.Node != "*redis.Cache" {
		t.Fatalf("errors.As found %q, want the node that failed", ne.Node)
	}
	var got servo.Report
	if !errors.As(err, &got) || len(got.Nodes) != 1 || got.Nodes[0].Name != "*postgres.DB" {
		t.Fatalf("errors.As(&servo.Report) = %v, want the rollback report", got)
	}
	want := "*redis.Cache: Init: connection refused\nrollback: *postgres.DB: abandoned: context deadline exceeded"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}
