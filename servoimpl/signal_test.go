package servoimpl

import (
	"bytes"
	"os"
	"syscall"
	"testing"
)

type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

func TestSignalExitCode(t *testing.T) {
	for _, tc := range []struct {
		sig  os.Signal
		want int
	}{
		{os.Interrupt, 130},
		{syscall.SIGTERM, 143},
		// Not a syscall.Signal, so there is no number to add.
		{fakeSignal{}, 1},
	} {
		if got := signalExitCode(tc.sig); got != tc.want {
			t.Errorf("signalExitCode(%v) = %d, want %d", tc.sig, got, tc.want)
		}
	}
}

// TestForceExitSaysWhyAndExitsBySignal: the second-signal exit used to be
// a silent os.Exit(1) — nothing in the log, and a status no different
// from an ordinary failure. It now names the signal on one line and exits
// 128 plus its number.
//
// Not parallel: it replaces the package's exit and stderr.
func TestForceExitSaysWhyAndExitsBySignal(t *testing.T) {
	prevExit, prevStderr := exit, stderr
	t.Cleanup(func() { exit, stderr = prevExit, prevStderr })

	var out bytes.Buffer
	code := -1
	exit = func(c int) { code = c }
	stderr = &out

	ForceExit(syscall.SIGTERM)

	if code != 143 {
		t.Fatalf("exit code = %d, want 143", code)
	}
	if got, want := out.String(), "servo: terminated signal received during shutdown, exiting with status 143 without finishing it\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}
