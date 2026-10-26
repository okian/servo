package servoimpl

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// ForceExit ends the process for a signal that arrived while Shutdown was
// running. The generated Shutdown calls it from the watcher it installs for
// the duration of the call: the first signal is what started the shutdown,
// and a second one means the operator has stopped waiting for it.
//
// It writes one line to stderr naming the signal, then exits with 128 plus
// the signal's number — 130 for SIGINT, 143 for SIGTERM — which is how a
// shell reports a process a signal killed. It used to be a silent
// os.Exit(1): nothing in the log said the unwind had been cut short, and
// nothing in the exit status told an operator's own interrupt apart from
// an ordinary failure.
func ForceExit(sig os.Signal) {
	code := signalExitCode(sig)
	// Nothing to do about a failed write: the process is leaving either way.
	_, _ = fmt.Fprintf(stderr, "servo: %v signal received during shutdown, exiting with status %d without finishing it\n", sig, code)
	exit(code)
}

// signalExitCode is 128 plus the signal's number, the shell's convention
// for a process ended by a signal. Every signal the generated watcher
// subscribes to — os.Interrupt and syscall.SIGTERM — is a syscall.Signal on
// every platform the generated file compiles for; 1 covers the rest.
func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}

// Replaced by tests, which cannot observe an os.Exit from inside the
// process making it.
var (
	exit             = os.Exit
	stderr io.Writer = os.Stderr
)
