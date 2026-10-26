//go:build unix

package servo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMainAnswersSIGHUPDuringShutdown: the generated Shutdown's watcher
// covers SIGINT and SIGTERM only, so Main keeps answering SIGHUP to the
// end — without it, Go's default would end the process mid-unwind with no
// word at all. Once Shutdown has begun, any signal means the operator has
// stopped waiting, first or not, as it does for that watcher.
func TestMainAnswersSIGHUPDuringShutdown(t *testing.T) {
	for _, tc := range []struct {
		name string
		// signalled is whether a first signal started the stop, rather
		// than Run returning on its own.
		signalled bool
	}{
		{"after a first signal", true},
		{"with Shutdown begun by Run returning", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			running, inShutdown, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			a := &fakeApp{
				run: func(ctx context.Context) error {
					close(running)
					if tc.signalled {
						return untilCancelled(ctx)
					}
					return nil
				},
				shutdown: func(context.Context) Report {
					close(inShutdown)
					<-release
					return Report{}
				},
			}
			status := h.start(app(a))
			<-running
			if tc.signalled {
				h.sigs.send(syscall.SIGTERM)
			}
			<-inShutdown
			h.sigs.send(syscall.SIGHUP)
			select {
			case code := <-h.exits:
				if code != 129 {
					t.Fatalf("exit status = %d, want 129", code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("SIGHUP during Shutdown did not end the process")
			}
			want := "servo: hangup signal received during shutdown, exiting with status 129 without finishing it\n"
			if got := h.stderr.String(); got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
			close(release)
			h.status(status)
		})
	}
}

// The tests below run Main for real, in a child process: this test binary
// re-executed with mainHelperEnv naming a scenario, so the signals are the
// kernel's and the exit status is the process's own.

const mainHelperEnv = "SERVO_MAIN_HELPER"

// scriptedApp is a scenario's App.
type scriptedApp struct {
	run      func(context.Context) error
	shutdown func(context.Context) Report
}

func (a *scriptedApp) Run(ctx context.Context) error { return a.run(ctx) }

func (a *scriptedApp) Shutdown(ctx context.Context) Report {
	if a.shutdown == nil {
		return Report{}
	}
	return a.shutdown(ctx)
}

// say tells the parent where the child has got to. Stdout is unbuffered,
// so the line is out before say returns.
func say(line string) { fmt.Println(line) }

func runThenCancel(ctx context.Context) error {
	say("running")
	return untilCancelled(ctx)
}

var errInit = &NodeError{Node: "*postgres.DB", Phase: "Init", Err: errors.New("dial tcp 127.0.0.1:5432: connect: connection refused")}

var mainScenarios = map[string]func(context.Context) (*scriptedApp, error){
	"serve": func(context.Context) (*scriptedApp, error) {
		return &scriptedApp{run: runThenCancel}, nil
	},
	// A runner that ignores its context, so Run never returns.
	"stubborn": func(context.Context) (*scriptedApp, error) {
		return &scriptedApp{run: func(ctx context.Context) error {
			say("running")
			<-ctx.Done()
			say("stopping")
			select {}
		}}, nil
	},
	// A Shutdown that never finishes.
	"slow-shutdown": func(context.Context) (*scriptedApp, error) {
		return &scriptedApp{run: runThenCancel, shutdown: func(context.Context) Report {
			say("shutting down")
			select {}
		}}, nil
	},
	// A Shutdown carrying its own second-signal watcher, as the generated
	// one does, with an exit status of its own to tell the two apart.
	"own-watcher": func(context.Context) (*scriptedApp, error) {
		return &scriptedApp{run: runThenCancel, shutdown: func(context.Context) Report {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt, syscall.SIGTERM)
			say("shutting down")
			fmt.Fprintf(os.Stderr, "shutdown's watcher: %v\n", <-c)
			os.Exit(200)
			return Report{}
		}}, nil
	},
	"new-fails": func(context.Context) (*scriptedApp, error) {
		return nil, errInit
	},
	// The tutorial's logger: JSON on stdout at WARN, installed as the
	// default while New builds the App. log.Fatal went through it at
	// INFO, so this startup failure printed nothing at all.
	"new-fails-behind-a-warn-logger": func(context.Context) (*scriptedApp, error) {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn})))
		return nil, errInit
	},
	"run-fails": func(context.Context) (*scriptedApp, error) {
		return &scriptedApp{run: func(context.Context) error {
			return &NodeError{Node: "http", Phase: "Run", Err: errors.New("listen tcp :8080: bind: address already in use")}
		}}, nil
	},
	"new-fails-with-options": func(context.Context) (*scriptedApp, error) {
		return nil, errInit
	},
}

// mainScenarioOptions are the MainOptions a scenario hands Main, beside
// its newApp: a zero MainOption, which sets nothing, and a Logger writing
// text to stdout.
var mainScenarioOptions = map[string]func() []MainOption{
	"new-fails-with-options": func() []MainOption {
		return []MainOption{{}, Logger(slog.New(slog.NewTextHandler(os.Stdout, nil)))}
	},
}

// TestMainHelperProcess is the child. In an ordinary run it does nothing;
// run by startChild, it hands one scenario's newApp to Main, which exits
// the process.
func TestMainHelperProcess(t *testing.T) {
	scenario := os.Getenv(mainHelperEnv)
	if scenario == "" {
		return
	}
	newApp, ok := mainScenarios[scenario]
	if !ok {
		fmt.Fprintf(os.Stderr, "no scenario %q\n", scenario)
		os.Exit(99)
	}
	var opts []MainOption
	if options, ok := mainScenarioOptions[scenario]; ok {
		opts = options()
	}
	Main(newApp, opts...)
}

type child struct {
	t      *testing.T
	cmd    *exec.Cmd
	lines  chan string
	stdout *syncBuffer
	stderr *syncBuffer
}

func startChild(t *testing.T, scenario string) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelperProcess$", "-test.timeout=2m")
	cmd.Env = append(os.Environ(), mainHelperEnv+"="+scenario)
	c := &child{t: t, cmd: cmd, lines: make(chan string, 16), stdout: &syncBuffer{}, stderr: &syncBuffer{}}
	cmd.Stderr = c.stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// A child left blocked by a failed test is killed rather than leaked.
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	go func() {
		defer close(c.lines)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			_, _ = fmt.Fprintln(c.stdout, sc.Text()) // a copy for the failure log; losing it loses nothing
			c.lines <- sc.Text()
		}
	}()
	return c
}

// await blocks until the child prints line.
func (c *child) await(line string) {
	c.t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case got, ok := <-c.lines:
			if !ok {
				c.t.Fatalf("the child exited before printing %q; stderr: %q", line, c.stderr.String())
			}
			if got == line {
				return
			}
		case <-timeout:
			c.t.Fatalf("the child did not print %q; stderr: %q", line, c.stderr.String())
		}
	}
}

func (c *child) signal(sig os.Signal) {
	c.t.Helper()
	if err := c.cmd.Process.Signal(sig); err != nil {
		c.t.Fatal(err)
	}
}

// wait returns the child's exit status, or the signal that killed it.
func (c *child) wait() (code int, killedBy syscall.Signal) {
	c.t.Helper()
	done := make(chan error, 1)
	go func() {
		for range c.lines {
			// Read to the end: Wait must not run before the pipe is drained.
		}
		done <- c.cmd.Wait()
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		c.t.Fatalf("the child did not exit; stderr: %q", c.stderr.String())
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, 0
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return -1, ws.Signal()
		}
		return exit.ExitCode(), 0
	default:
		c.t.Fatal(err)
		return -1, 0
	}
}

func (c *child) wantExit(want int) {
	c.t.Helper()
	code, killedBy := c.wait()
	if killedBy != 0 {
		c.t.Fatalf("the child was killed by %v, want exit status %d; stderr: %q", killedBy, want, c.stderr.String())
	}
	if code != want {
		c.t.Fatalf("exit status = %d, want %d; stderr: %q", code, want, c.stderr.String())
	}
}

// TestMainProcessStopsCleanlyOnEachSignal: each of the three exits 0 after
// an orderly stop. SIGHUP used to end the process outright — Go's default
// — because nothing subscribed to it.
func TestMainProcessStopsCleanlyOnEachSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()
			c := startChild(t, "serve")
			c.await("running")
			c.signal(sig)
			c.wantExit(0)
			if got := c.stderr.String(); got != "" {
				t.Fatalf("stderr = %q, want nothing", got)
			}
		})
	}
}

// TestMainProcessSecondSignalExits: a second signal while a runner is
// still winding down ends the process with 128 plus its number and one
// line saying so. It used to be swallowed until Shutdown began.
func TestMainProcessSecondSignalExits(t *testing.T) {
	t.Parallel()
	c := startChild(t, "stubborn")
	c.await("running")
	c.signal(syscall.SIGTERM)
	c.await("stopping")
	c.signal(syscall.SIGINT)
	c.wantExit(130)
	if got, want := c.stderr.String(), "servo: interrupt signal received during shutdown, exiting with status 130 without finishing it\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestMainProcessSIGHUPDuringShutdownExits(t *testing.T) {
	t.Parallel()
	c := startChild(t, "slow-shutdown")
	c.await("running")
	c.signal(syscall.SIGTERM)
	c.await("shutting down")
	c.signal(syscall.SIGHUP)
	c.wantExit(129)
	if got, want := c.stderr.String(), "servo: hangup signal received during shutdown, exiting with status 129 without finishing it\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// TestMainProcessLeavesSIGTERMToShutdownsWatcher: during Shutdown, a
// second SIGTERM is answered by Shutdown's own watcher alone. Had Main
// kept its subscription, both would answer: two lines, and a race over
// which exit status the process ends with.
func TestMainProcessLeavesSIGTERMToShutdownsWatcher(t *testing.T) {
	t.Parallel()
	c := startChild(t, "own-watcher")
	c.await("running")
	c.signal(syscall.SIGTERM)
	c.await("shutting down")
	c.signal(syscall.SIGTERM)
	c.wantExit(200)
	if got, want := c.stderr.String(), "shutdown's watcher: terminated\n"; got != want {
		t.Fatalf("stderr = %q, want only %q", got, want)
	}
}

func TestMainProcessStartupFailure(t *testing.T) {
	t.Parallel()
	c := startChild(t, "new-fails")
	c.wantExit(1)
	if got, want := c.stderr.String(), `ERROR servo: startup failed err="*postgres.DB: Init: dial tcp 127.0.0.1:5432: connect: connection refused"`; !strings.Contains(got, want) {
		t.Fatalf("stderr = %q, want a line containing %q", got, want)
	}
}

// TestMainProcessStartupFailureBehindAWarnLogger is the tutorial's silent
// failure: the App's own WARN-level JSON logger, installed as the slog
// default, now carries the ERROR line out on stdout.
func TestMainProcessStartupFailureBehindAWarnLogger(t *testing.T) {
	t.Parallel()
	c := startChild(t, "new-fails-behind-a-warn-logger")
	c.wantExit(1)
	if got, want := c.stdout.String(), `"level":"ERROR","msg":"servo: startup failed","err":"*postgres.DB: Init: dial tcp 127.0.0.1:5432: connect: connection refused"`; !strings.Contains(got, want) {
		t.Fatalf("stdout = %q, want a line containing %q", got, want)
	}
}

func TestMainProcessRunFailure(t *testing.T) {
	t.Parallel()
	c := startChild(t, "run-fails")
	c.wantExit(1)
	if got, want := c.stderr.String(), `ERROR servo: run failed err="http: Run: listen tcp :8080: bind: address already in use"`; !strings.Contains(got, want) {
		t.Fatalf("stderr = %q, want a line containing %q", got, want)
	}
}

// TestMainProcessAppliesItsOptions: what Main is handed reaches the run.
// The Logger carries the startup failure out on stdout, leaving stderr
// empty, and the zero MainOption beside it, which sets nothing, is
// skipped rather than called.
func TestMainProcessAppliesItsOptions(t *testing.T) {
	t.Parallel()
	c := startChild(t, "new-fails-with-options")
	c.wantExit(1)
	if got, want := c.stdout.String(), `level=ERROR msg="servo: startup failed" err="*postgres.DB: Init: dial tcp 127.0.0.1:5432: connect: connection refused"`; !strings.Contains(got, want) {
		t.Fatalf("stdout = %q, want a line containing %q", got, want)
	}
	if got := c.stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want nothing: the Logger option took the line", got)
	}
}
