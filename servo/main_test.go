package servo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// The tests here drive Main's core, mainOptions.run, with the process-level
// calls replaced: signals come from fakeSignals, exits are recorded rather
// than made, and stderr and the log are buffers. What only a real process
// can show — a real signal, a real exit status — is in main_unix_test.go.

// fakeSignals stands in for os/signal. notify and stop keep the
// subscription table the runtime would, and send delivers a signal to
// every channel still subscribed to it, without blocking, as the runtime
// does.
type fakeSignals struct {
	mu   sync.Mutex
	subs map[chan<- os.Signal][]os.Signal
	// log records each subscription change in order, so a test can ask
	// what was subscribed when.
	log []string
}

func newFakeSignals() *fakeSignals {
	return &fakeSignals{subs: map[chan<- os.Signal][]os.Signal{}}
}

func (f *fakeSignals) notify(c chan<- os.Signal, sigs ...os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[c] = append(f.subs[c], sigs...)
	f.log = append(f.log, fmt.Sprintf("notify %v", sigs))
}

func (f *fakeSignals) stop(c chan<- os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sigs, ok := f.subs[c]; ok {
		f.log = append(f.log, fmt.Sprintf("stop %v", sigs))
	}
	delete(f.subs, c)
}

// subscribed reports whether any channel is subscribed to sig.
func (f *fakeSignals) subscribed(sig os.Signal) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sigs := range f.subs {
		if slices.Contains(sigs, sig) {
			return true
		}
	}
	return false
}

func (f *fakeSignals) send(sig os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c, sigs := range f.subs {
		if slices.Contains(sigs, sig) {
			select {
			case c <- sig:
			default:
			}
		}
	}
}

// syncBuffer is a bytes.Buffer safe to write from Main's goroutines while
// the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type harness struct {
	t      *testing.T
	sigs   *fakeSignals
	stderr *syncBuffer
	logs   *syncBuffer
	exits  chan int
	o      *mainOptions
}

func newHarness(t *testing.T, opts ...MainOption) *harness {
	t.Helper()
	// Main's watcher goroutine must be gone by the time run returns.
	before := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, before) })
	h := &harness{
		t:      t,
		sigs:   newFakeSignals(),
		stderr: &syncBuffer{},
		logs:   &syncBuffer{},
		exits:  make(chan int, 4),
	}
	h.o = &mainOptions{
		shutdownTimeout: defaultShutdownTimeout,
		logger:          slog.New(slog.NewTextHandler(h.logs, nil)),
		notify:          h.sigs.notify,
		stop:            h.sigs.stop,
		exit:            func(code int) { h.exits <- code },
		stderr:          h.stderr,
	}
	for _, opt := range opts {
		opt.apply(h.o)
	}
	return h
}

// start runs Main's core on a goroutine of its own and returns the
// channel its status arrives on.
func (h *harness) start(newApp func(context.Context) (mainApp, error)) <-chan int {
	status := make(chan int, 1)
	go func() { status <- h.o.run(newApp) }()
	return status
}

func (h *harness) status(status <-chan int) int {
	h.t.Helper()
	select {
	case code := <-status:
		return code
	case <-time.After(10 * time.Second):
		h.t.Fatal("Main's core did not return")
		return -1
	}
}

func (h *harness) noExit() {
	h.t.Helper()
	select {
	case code := <-h.exits:
		h.t.Fatalf("the process was ended with status %d; stderr: %q", code, h.stderr.String())
	default:
	}
}

// app wraps a fake App as the newApp Main is handed.
func app(a *fakeApp) func(context.Context) (mainApp, error) {
	return func(context.Context) (mainApp, error) { return a, nil }
}

// fakeApp is the generated App's Run and Shutdown, scripted.
type fakeApp struct {
	run      func(context.Context) error
	shutdown func(context.Context) Report

	mu          sync.Mutex
	shutdownCtx context.Context
}

func (a *fakeApp) Run(ctx context.Context) error {
	if a.run == nil {
		return nil
	}
	return a.run(ctx)
}

func (a *fakeApp) Shutdown(ctx context.Context) Report {
	a.mu.Lock()
	a.shutdownCtx = ctx
	a.mu.Unlock()
	if a.shutdown == nil {
		return Report{}
	}
	return a.shutdown(ctx)
}

func (a *fakeApp) lastShutdownCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.shutdownCtx
}

// untilCancelled is the ordinary runner: it runs until the signal and
// hands back the cancellation.
func untilCancelled(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestMainStartupFailureIsLoggedAndExits1(t *testing.T) {
	h := newHarness(t)
	initErr := &NodeError{Node: "*postgres.DB", Phase: "Init", Err: errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")}

	code := h.o.run(func(context.Context) (mainApp, error) { return nil, initErr })

	if code != 1 {
		t.Fatalf("status = %d, want 1", code)
	}
	want := `level=ERROR msg="servo: startup failed" err="*postgres.DB: Init: dial tcp 127.0.0.1:5432: connect: connection refused"`
	if got := h.logs.String(); !strings.Contains(got, want) {
		t.Fatalf("log = %q, want a line containing %q", got, want)
	}
	if got := h.stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want nothing: the logger took the line", got)
	}
}

// TestMainSubscribesBeforeNewApp: a signal during New must reach Main. Had
// newApp run first, a SIGTERM arriving while it connected to a database
// would find nothing subscribed and end the process by Go's default, with
// no rollback.
func TestMainSubscribesBeforeNewApp(t *testing.T) {
	h := newHarness(t)
	var during []bool
	code := h.o.run(func(context.Context) (mainApp, error) {
		during = []bool{h.sigs.subscribed(os.Interrupt), h.sigs.subscribed(syscall.SIGTERM)}
		return &fakeApp{}, nil
	})
	if code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
	if !slices.Equal(during, []bool{true, true}) {
		t.Fatalf("subscribed to SIGINT, SIGTERM while newApp ran: %v, want both", during)
	}
}

func TestMainCleanStopOnTheSignal(t *testing.T) {
	h := newHarness(t)
	running := make(chan struct{})
	var runCause error
	a := &fakeApp{run: func(ctx context.Context) error {
		close(running)
		<-ctx.Done()
		runCause = context.Cause(ctx)
		return ctx.Err()
	}}

	status := h.start(app(a))
	<-running
	sent := time.Now()
	h.sigs.send(syscall.SIGTERM)

	if code := h.status(status); code != 0 {
		t.Fatalf("status = %d, want 0; log: %q", code, h.logs.String())
	}
	h.noExit()
	if got := h.logs.String() + h.stderr.String(); got != "" {
		t.Fatalf("output = %q, want none for a clean stop", got)
	}
	// The cause reads the way signal.NotifyContext's does, and still
	// counts as a cancellation.
	if runCause == nil || runCause.Error() != "terminated signal received" || !errors.Is(runCause, context.Canceled) {
		t.Fatalf("context.Cause = %v, want \"terminated signal received\", matching context.Canceled", runCause)
	}

	// Shutdown's context is bounded by a deadline counted from the signal.
	sctx := a.lastShutdownCtx()
	if sctx == nil {
		t.Fatal("Shutdown was not called")
	}
	deadline, ok := sctx.Deadline()
	if !ok {
		t.Fatal("Shutdown's context has no deadline")
	}
	if early, late := sent.Add(defaultShutdownTimeout), sent.Add(defaultShutdownTimeout+time.Second); deadline.Before(early) || deadline.After(late) {
		t.Fatalf("Shutdown deadline = %v after the signal, want %v", deadline.Sub(sent), defaultShutdownTimeout)
	}
}

func TestMainShutdownContextIsNotRunsContext(t *testing.T) {
	h := newHarness(t)
	running := make(chan struct{})
	var errAtShutdown error
	a := &fakeApp{
		run: func(ctx context.Context) error { close(running); return untilCancelled(ctx) },
		shutdown: func(ctx context.Context) Report {
			errAtShutdown = ctx.Err()
			return Report{}
		},
	}
	status := h.start(app(a))
	<-running
	h.sigs.send(os.Interrupt)
	if code := h.status(status); code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
	if errAtShutdown != nil {
		t.Fatalf("Shutdown's context was already done when Shutdown began: %v", errAtShutdown)
	}
}

// TestMainShutdownDeadlineCountsFromTheSignal: Run's own wind-down spends
// from the same budget, so the whole stop fits in the window a supervisor
// gives — a runner taking its full DefaultRunGrace does not push the end
// of Shutdown past it.
func TestMainShutdownDeadlineCountsFromTheSignal(t *testing.T) {
	h := newHarness(t, ShutdownTimeout(2*time.Second))
	running := make(chan struct{})
	a := &fakeApp{run: func(ctx context.Context) error {
		close(running)
		<-ctx.Done()
		time.Sleep(300 * time.Millisecond) // a slow wind-down
		return ctx.Err()
	}}
	status := h.start(app(a))
	<-running
	sent := time.Now()
	h.sigs.send(syscall.SIGTERM)
	if code := h.status(status); code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
	deadline, _ := a.lastShutdownCtx().Deadline()
	if got := deadline.Sub(sent); got < 2*time.Second || got > 2*time.Second+200*time.Millisecond {
		t.Fatalf("Shutdown deadline = %v after the signal, want 2s: counted from Run's return it would be 2.3s", got)
	}
}

// TestMainShutdownDeadlineWithoutASignal: Run returning on its own — a
// finite job done, a runner failed — starts the stop then.
func TestMainShutdownDeadlineWithoutASignal(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []MainOption
		want time.Duration
	}{
		{"default", nil, defaultShutdownTimeout},
		{"set", []MainOption{ShutdownTimeout(40 * time.Second)}, 40 * time.Second},
		// Zero would otherwise mean an expired deadline or none at all,
		// and Main never shuts down without one.
		{"zero keeps the default", []MainOption{ShutdownTimeout(0)}, defaultShutdownTimeout},
		{"negative keeps the default", []MainOption{ShutdownTimeout(-time.Second)}, defaultShutdownTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.opts...)
			var returned time.Time
			a := &fakeApp{run: func(context.Context) error {
				returned = time.Now()
				return nil
			}}
			if code := h.o.run(app(a)); code != 0 {
				t.Fatalf("status = %d, want 0", code)
			}
			deadline, ok := a.lastShutdownCtx().Deadline()
			if !ok {
				t.Fatal("Shutdown's context has no deadline")
			}
			if got := deadline.Sub(returned); got < tc.want || got > tc.want+time.Second {
				t.Fatalf("Shutdown deadline = %v after Run returned, want %v", got, tc.want)
			}
		})
	}
}

func TestMainRunFailureIsLoggedAndShutdownStillRuns(t *testing.T) {
	h := newHarness(t)
	runErr := &NodeError{Node: "*worker.Consumer", Phase: "Run", Err: errors.New("broker unreachable")}
	shutdown := false
	a := &fakeApp{
		run:      func(context.Context) error { return runErr },
		shutdown: func(context.Context) Report { shutdown = true; return Report{} },
	}
	if code := h.o.run(app(a)); code != 1 {
		t.Fatalf("status = %d, want 1", code)
	}
	if !shutdown {
		t.Fatal("Shutdown did not run after Run failed")
	}
	want := `level=ERROR msg="servo: run failed" err="*worker.Consumer: Run: broker unreachable"`
	if got := h.logs.String(); !strings.Contains(got, want) {
		t.Fatalf("log = %q, want a line containing %q", got, want)
	}
}

func TestMainUncleanShutdownIsLoggedAndExits1(t *testing.T) {
	h := newHarness(t)
	a := &fakeApp{shutdown: func(context.Context) Report {
		return Report{Nodes: []NodeResult{
			{Name: "*api.Server", Status: StatusOK},
			{Name: "*postgres.DB", Status: StatusAbandoned, Err: context.DeadlineExceeded},
		}}
	}}
	if code := h.o.run(app(a)); code != 1 {
		t.Fatalf("status = %d, want 1", code)
	}
	want := `level=ERROR msg="servo: shutdown was not clean" err="*postgres.DB: abandoned: context deadline exceeded"`
	if got := h.logs.String(); !strings.Contains(got, want) {
		t.Fatalf("log = %q, want a line containing %q", got, want)
	}
}

// startupInterrupted is the error the generated New returns when the
// signal arrives during Init: servoimpl.Init's wording, wrapping
// context.Cause.
func startupInterrupted(ctx context.Context) error {
	return fmt.Errorf("servo: startup interrupted: %w", context.Cause(ctx))
}

// rolledBack is what servoimpl.RolledBack makes of a startup failure whose
// unwind did not come out clean: the failure, carrying the unwind's report
// for errors.As alone.
type rolledBack struct {
	err      error
	rollback Report
}

func (e *rolledBack) Error() string { return e.err.Error() + "\nrollback: " + e.rollback.Error() }
func (e *rolledBack) Unwrap() error { return e.err }
func (e *rolledBack) As(target any) bool {
	if r, ok := target.(*Report); ok {
		*r = e.rollback
		return true
	}
	return false
}

// TestMainInterruptedStartup: a signal during New is the stop the operator
// asked for, arriving early. New rolls back and says so, and Main exits 0
// as it would a moment later, during Run. Anything more than the
// interruption is still a failure.
func TestMainInterruptedStartup(t *testing.T) {
	unclean := Report{Nodes: []NodeResult{{Name: "*postgres.DB", Status: StatusAbandoned, Err: context.DeadlineExceeded}}}
	for _, tc := range []struct {
		name string
		// signal is whether a signal arrives while newApp runs.
		signal  bool
		failure func(ctx context.Context) error
		want    int
	}{
		{"interrupted, clean rollback", true, startupInterrupted, 0},
		{"interrupted, unclean rollback", true, func(ctx context.Context) error {
			return &rolledBack{err: startupInterrupted(ctx), rollback: unclean}
		}, 1},
		{"interrupted, and an Init failed besides", true, func(ctx context.Context) error {
			return errors.Join(startupInterrupted(ctx), &NodeError{Node: "*redis.Cache", Phase: "Init", Err: errors.New("connection refused")})
		}, 1},
		// A cancellation with no signal behind it is newApp's own failure.
		{"a cancellation nobody signalled", false, func(context.Context) error { return context.Canceled }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			status := h.start(func(ctx context.Context) (mainApp, error) {
				if tc.signal {
					h.sigs.send(syscall.SIGTERM)
					<-ctx.Done()
				}
				return nil, tc.failure(ctx)
			})
			code := h.status(status)
			if code != tc.want {
				t.Fatalf("status = %d, want %d; log: %q", code, tc.want, h.logs.String())
			}
			logged := strings.Contains(h.logs.String(), `msg="servo: startup failed"`)
			if logged != (tc.want == 1) {
				t.Fatalf("logged = %v, want %v; log: %q", logged, tc.want == 1, h.logs.String())
			}
		})
	}
}

// TestMainSecondSignalEndsTheProcess: a second signal before Shutdown
// begins — here while Run is still winding down — used to be swallowed by
// the first's subscription until Shutdown installed its own watcher. It
// now ends the process at once, in the generated watcher's words.
func TestMainSecondSignalEndsTheProcess(t *testing.T) {
	h := newHarness(t)
	running, stopping, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a := &fakeApp{run: func(ctx context.Context) error {
		close(running)
		<-ctx.Done()
		close(stopping)
		<-release // a runner slow to stop
		return ctx.Err()
	}}
	status := h.start(app(a))
	<-running
	h.sigs.send(syscall.SIGTERM)
	<-stopping
	h.sigs.send(os.Interrupt)

	select {
	case code := <-h.exits:
		if code != 130 {
			t.Fatalf("exit status = %d, want 130", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second signal did not end the process")
	}
	want := "servo: interrupt signal received during shutdown, exiting with status 130 without finishing it\n"
	if got := h.stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	close(release)
	h.status(status)
}

// TestMainHandsSIGINTAndSIGTERMToShutdown: once Shutdown begins, the
// generated Shutdown's own watcher answers those two, so Main must have
// let go of them — both answering would print twice and race to exit.
func TestMainHandsSIGINTAndSIGTERMToShutdown(t *testing.T) {
	h := newHarness(t)
	running, inShutdown, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var subscribed []bool
	a := &fakeApp{
		run: func(ctx context.Context) error { close(running); return untilCancelled(ctx) },
		shutdown: func(context.Context) Report {
			subscribed = []bool{h.sigs.subscribed(os.Interrupt), h.sigs.subscribed(syscall.SIGTERM)}
			close(inShutdown)
			<-release
			return Report{}
		},
	}
	status := h.start(app(a))
	<-running
	h.sigs.send(syscall.SIGTERM)
	<-inShutdown
	if !slices.Equal(subscribed, []bool{false, false}) {
		t.Fatalf("Main subscribed to SIGINT, SIGTERM during Shutdown: %v, want neither", subscribed)
	}
	h.sigs.send(os.Interrupt)
	h.sigs.send(syscall.SIGTERM)
	close(release)
	if code := h.status(status); code != 0 {
		t.Fatalf("status = %d, want 0", code)
	}
	h.noExit()
}

func TestMainReleasesItsSignals(t *testing.T) {
	h := newHarness(t)
	h.o.run(app(&fakeApp{}))
	h.sigs.mu.Lock()
	defer h.sigs.mu.Unlock()
	if len(h.sigs.subs) != 0 {
		t.Fatalf("still subscribed after Main finished: %v", h.sigs.subs)
	}
}

// failingHandler is a handler whose writes fail: a closed pipe, a full
// disk.
type failingHandler struct{ slog.Handler }

func (failingHandler) Handle(context.Context, slog.Record) error {
	return errors.New("write: broken pipe")
}

// TestMainFallsBackToStderr: a line the logger would drop goes to stderr,
// since Main exits 1 right after it and silence is the failure Main
// exists to end.
func TestMainFallsBackToStderr(t *testing.T) {
	for _, tc := range []struct {
		name   string
		logger *slog.Logger
	}{
		{"handler not enabled for ERROR", slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 4}))},
		{"handler fails to write", slog.New(failingHandler{slog.NewTextHandler(&bytes.Buffer{}, nil)})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Logger(tc.logger))
			code := h.o.run(func(context.Context) (mainApp, error) {
				return nil, errors.New("postgres: ping: connection refused")
			})
			if code != 1 {
				t.Fatalf("status = %d, want 1", code)
			}
			if got, want := h.stderr.String(), "servo: startup failed: postgres: ping: connection refused\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
		})
	}
}

// TestMainLogsThroughTheDefaultAsItIsThen: without a Logger option, Main
// uses slog.Default() as it is when the line is written, not when Main
// was called — so the logger an App installs while New builds it, as the
// tutorial's does, is the one a failure is reported through.
//
// Not parallel: it replaces the slog and log defaults.
func TestMainLogsThroughTheDefaultAsItIsThen(t *testing.T) {
	prevSlog, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	h := newHarness(t, Logger(nil))
	installed := &syncBuffer{}
	code := h.o.run(func(context.Context) (mainApp, error) {
		// The tutorial's NewLogger: JSON, at the level the operator set.
		slog.SetDefault(slog.New(slog.NewJSONHandler(installed, &slog.HandlerOptions{Level: slog.LevelWarn})))
		return nil, errors.New("natsbroker: connect: nats: no servers available for connection")
	})
	if code != 1 {
		t.Fatalf("status = %d, want 1", code)
	}
	if got := installed.String(); !strings.Contains(got, `"level":"ERROR","msg":"servo: startup failed","err":"natsbroker: connect: nats: no servers available for connection"`) {
		t.Fatalf("installed logger got %q, want the ERROR line", got)
	}
	if got := h.stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want nothing: the logger took the line", got)
	}
}

func TestSignalCause(t *testing.T) {
	c := signalCause{syscall.SIGTERM}
	if got, want := c.Error(), "terminated signal received"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(c, context.Canceled) {
		t.Fatal("errors.Is(cause, context.Canceled) = false")
	}
	if errors.Is(c, context.DeadlineExceeded) {
		t.Fatal("errors.Is(cause, context.DeadlineExceeded) = true")
	}
}
