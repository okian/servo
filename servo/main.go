package servo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// defaultShutdownTimeout is how long Main gives a stop, from the first
// signal to the end of Shutdown, unless ShutdownTimeout says otherwise. It
// sits below the 30 seconds Kubernetes allows a pod between its SIGTERM
// and its SIGKILL by default, so the unwind ends inside that window with a
// margin to spare rather than racing it.
const defaultShutdownTimeout = 25 * time.Second

// Main is the whole of a service's main function:
//
//	func main() { servo.Main(New) }
//
// It builds the App with newApp, runs it until the process is told to
// stop, shuts it down under a deadline, logs every failure on the way, and
// exits with ExitCode's status: 0 for a clean run and a clean stop, 1 for
// anything else. A graph that declares a servo.Value passes a closure
// calling NewWith:
//
//	servo.Main(func(ctx context.Context) (*App, error) {
//		return NewWith(ctx, Values{Target: target})
//	})
//
// SIGINT, SIGTERM and SIGHUP each ask for a graceful stop: they cancel the
// context newApp and Run were given, so a startup still in progress rolls
// back and a running App's runners return. SIGHUP is on the list because
// Go's own default for it ends the process on the spot, and it is what a
// closed terminal or a dropped ssh session sends: a service started from
// one would die mid-request, its connections cut and nothing flushed.
// SIGQUIT is left alone; its default dumps every goroutine's stack, which
// is what a stop that seems stuck calls for.
//
// A second signal ends the process at once, with one line on stderr naming
// it and the status a shell reports for a process a signal killed: 128
// plus its number, so 130 for SIGINT and 143 for SIGTERM. Main watches for
// it from the first signal until Shutdown begins. From then on the
// generated Shutdown's own watcher answers SIGINT and SIGTERM, in the same
// words, and Main stops listening for those two so only one of them
// speaks; it keeps SIGHUP, which that watcher does not cover.
//
// Every failure is logged at ERROR: newApp's error, a Run error other than
// the cancellation the signal caused, and a Shutdown report that is not
// clean. The line goes through the Logger option's logger or, without
// one, through slog.Default() as it is when the line is written, so a
// logger the App installs as the default while New builds it is the one
// that says why the process stopped. A line that logger would drop —
// its handler not enabled for ERROR, or failing to write — goes to stderr
// instead: the process never exits 1 without saying why.
//
// A startup the signal interrupts is not a failure. New rolls back and
// says so, and Main exits 0 without logging it, as it would had the signal
// come a moment later, during Run — unless the rollback did not come out
// clean.
//
// Shutdown gets a context of its own whose deadline is ShutdownTimeout
// after the first signal, or after Run returned, when it returned without
// one: 25 seconds by default. Not Run's context: that one is already
// cancelled — its cancellation is what started the stop — and a teardown
// handed it would abandon every node before beginning. Not an unbounded
// context.Background() either: a supervisor sends SIGKILL when its grace
// period runs out whether or not the unwind has finished. Counting from
// the signal keeps Run's own wind-down, up to DefaultRunGrace, inside the
// same budget, so the whole stop fits in the window the supervisor gives.
//
// newApp is normally the generated New; anything with the generated Run
// and Shutdown methods will do. Main never returns.
func Main[A interface {
	Run(context.Context) error
	Shutdown(context.Context) Report
}](newApp func(context.Context) (A, error), opts ...MainOption) {
	o := mainOptions{
		shutdownTimeout: defaultShutdownTimeout,
		notify:          signal.Notify,
		stop:            signal.Stop,
		exit:            os.Exit,
		stderr:          os.Stderr,
	}
	for _, opt := range opts {
		if opt.apply != nil {
			opt.apply(&o)
		}
	}
	o.exit(o.run(func(ctx context.Context) (mainApp, error) {
		app, err := newApp(ctx)
		return app, err
	}))
}

// MainOption configures Main. ShutdownTimeout and Logger make them.
type MainOption struct {
	apply func(*mainOptions)
}

// ShutdownTimeout sets how long Main gives a stop: from the first signal,
// or from Run returning on its own, to the end of Shutdown. It is the
// deadline of the context Shutdown receives, so every node still to stop
// when it passes is reported abandoned rather than waited for, and the
// process exits 1.
//
// The default, 25 seconds, fits inside the 30 seconds Kubernetes gives a
// pod by default. Whatever it is set to, keep it below the supervisor's
// grace period with a margin. Zero or less keeps the default: Main never
// shuts down without a deadline.
func ShutdownTimeout(d time.Duration) MainOption {
	return MainOption{apply: func(o *mainOptions) {
		if d > 0 {
			o.shutdownTimeout = d
		}
	}}
}

// Logger sets the logger Main reports failures through. Without it, or
// given nil, Main uses slog.Default() as it is when each line is written.
func Logger(l *slog.Logger) MainOption {
	return MainOption{apply: func(o *mainOptions) { o.logger = l }}
}

// mainOptions is what MainOption sets, plus the process-level calls Main
// makes, which tests replace: an in-process test can neither take a real
// signal safely nor survive os.Exit.
type mainOptions struct {
	shutdownTimeout time.Duration
	logger          *slog.Logger

	notify func(chan<- os.Signal, ...os.Signal)
	stop   func(chan<- os.Signal)
	exit   func(int)
	stderr io.Writer
}

// mainApp is the method set Main drives: the generated App's Run and
// Shutdown.
type mainApp interface {
	Run(context.Context) error
	Shutdown(context.Context) Report
}

// run is Main without the exit: everything Main does, returning the status
// it exits with.
func (o *mainOptions) run(newApp func(context.Context) (mainApp, error)) int {
	ctx, sigs := o.watchSignals()
	defer sigs.release()

	app, err := newApp(ctx)
	if err != nil {
		if ctx.Err() != nil && interruptedOnly(err) {
			return 0
		}
		o.logError("servo: startup failed", err)
		return 1
	}

	runErr := app.Run(ctx)
	if runErr != nil && !canceledOnly(runErr) {
		o.logError("servo: run failed", runErr)
	}

	sigs.handOver()
	sctx, cancel := context.WithDeadline(context.Background(), sigs.stopBegan().Add(o.shutdownTimeout))
	defer cancel()
	report := app.Shutdown(sctx)
	if !report.Clean() {
		o.logError("servo: shutdown was not clean", report)
	}
	return ExitCode(runErr, report)
}

// interruptedOnly reports whether newApp's error says only that the stop
// signal interrupted it: a cancellation, and no unclean rollback report
// riding along. The generated New attaches the rollback's report, for
// errors.As to find, only when it did not come out clean.
func interruptedOnly(err error) bool {
	var rollback Report
	if errors.As(err, &rollback) && !rollback.Clean() {
		return false
	}
	return canceledOnly(err)
}

// logError reports one failure at ERROR, and falls back to stderr when the
// logger would drop it. The obvious main's log.Fatal printed nothing at
// all once an App had installed a WARN-level handler as the slog default —
// SetDefault routes the log package through that handler at INFO — and
// exited 1 without a word; that silence is what this exists to end.
func (o *mainOptions) logError(msg string, err error) {
	logger := o.logger
	if logger == nil {
		logger = slog.Default()
	}
	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelError) {
		r := slog.NewRecord(time.Now(), slog.LevelError, msg, 0)
		r.AddAttrs(slog.Any("err", err))
		if logger.Handler().Handle(ctx, r) == nil {
			return
		}
	}
	// Nothing to do about a failed write: stderr is the last resort.
	_, _ = fmt.Fprintf(o.stderr, "%s: %v\n", msg, err)
}

// forceExit ends the process for a second signal the way the generated
// Shutdown's watcher does, and in its words, so an operator reads one
// message whichever of the two caught the signal.
func (o *mainOptions) forceExit(sig os.Signal) {
	code := signalExitCode(sig)
	// Nothing to do about a failed write: the process is leaving either way.
	_, _ = fmt.Fprintf(o.stderr, "servo: %v signal received during shutdown, exiting with status %d without finishing it\n", sig, code)
	o.exit(code)
}

// signals is Main's signal handling: one goroutine over two subscriptions,
// split by who answers a signal once Shutdown begins.
type signals struct {
	o      *mainOptions
	cancel context.CancelCauseFunc

	// stopping carries SIGINT and SIGTERM. Main unsubscribes it as
	// Shutdown begins: the generated Shutdown watches those two itself,
	// and with both subscribed, both would answer.
	stopping chan os.Signal
	// hangup carries SIGHUP, which the generated Shutdown does not watch,
	// so Main answers it to the end.
	hangup chan os.Signal

	shutdown chan struct{}             // closed as Shutdown begins
	done     chan struct{}             // closed as Main finishes
	stopAt   atomic.Pointer[time.Time] // when the first signal came
}

// watchSignals subscribes to the stop signals and returns the context they
// cancel. Main calls it before anything else, so no signal can arrive to
// find nothing subscribed and end the process by Go's default.
func (o *mainOptions) watchSignals() (context.Context, *signals) {
	ctx, cancel := context.WithCancelCause(context.Background())
	s := &signals{
		o:      o,
		cancel: cancel,
		// Room for the first signal and a second, so one sent hard on
		// the heels of the other is not dropped before the goroutine
		// below gets to run.
		stopping: make(chan os.Signal, 2),
		hangup:   make(chan os.Signal, 2),
		shutdown: make(chan struct{}),
		done:     make(chan struct{}),
	}
	o.notify(s.stopping, os.Interrupt, syscall.SIGTERM)
	// Notify with no signals named would subscribe to every signal there
	// is, and a port without SIGHUP names none here.
	if len(hangupSignals) > 0 {
		o.notify(s.hangup, hangupSignals...)
	}
	go s.loop()
	return ctx, s
}

// loop answers the first signal by cancelling the context, and any signal
// after it by ending the process — as it does any signal at all once
// Shutdown has begun, the rule the generated Shutdown's watcher follows.
//
// Whether Shutdown has begun is asked when a signal arrives, not raced
// against it in the select: with both ready, select picks either, and a
// signal sent during Shutdown could be taken for a first one and do
// nothing.
func (s *signals) loop() {
	stopping := false
	for {
		var sig os.Signal
		select {
		case sig = <-s.stopping:
		case sig = <-s.hangup:
		case <-s.done:
			return
		}
		if !stopping && !s.shutdownBegan() {
			stopping = true
			s.first(sig)
			continue
		}
		s.o.forceExit(sig)
		return
	}
}

func (s *signals) shutdownBegan() bool {
	select {
	case <-s.shutdown:
		return true
	default:
		return false
	}
}

func (s *signals) first(sig os.Signal) {
	now := time.Now()
	s.stopAt.Store(&now)
	s.cancel(signalCause{sig})
}

// handOver gives SIGINT and SIGTERM to the generated Shutdown, which is
// about to subscribe to them itself. signal.Stop guarantees no further
// delivery once it returns, so a signal reaches exactly one of the two
// watchers: Main's if it came before this, Shutdown's if it came after.
func (s *signals) handOver() {
	s.o.stop(s.stopping)
	close(s.shutdown)
}

// stopBegan is when the stop began: the first signal, or now, when Run
// returned without one.
func (s *signals) stopBegan() time.Time {
	if at := s.stopAt.Load(); at != nil {
		return *at
	}
	return time.Now()
}

func (s *signals) release() {
	s.o.stop(s.stopping)
	s.o.stop(s.hangup)
	close(s.done)
	s.cancel(nil)
}

// signalCause is the cause the first signal cancels the context with. It
// reads like signal.NotifyContext's — "terminated signal received" — and
// counts as context.Canceled for errors.Is the same way, so New's "startup
// interrupted" error, and a runner handing back context.Cause, both read
// as the cancellation they are.
type signalCause struct{ sig os.Signal }

func (c signalCause) Error() string        { return c.sig.String() + " signal received" }
func (c signalCause) Is(target error) bool { return target == context.Canceled }
