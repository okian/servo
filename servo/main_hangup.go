//go:build !js && !plan9

package servo

import (
	"os"
	"syscall"
)

// hangupSignals is what Main subscribes to beside SIGINT and SIGTERM, and
// answers itself even while Shutdown runs. It is a variable of its own,
// in a file of its own, because two ports lack it: js/wasm has no SIGHUP,
// and plan9 numbers no signals at all. They get main_nohangup.go instead.
var hangupSignals = []os.Signal{syscall.SIGHUP}

// signalExitCode is 128 plus the signal's number, the shell's convention
// for a process a signal ended, and the status the generated Shutdown's
// watcher exits with too. Every signal Main subscribes to is a
// syscall.Signal on the ports this file builds for; 1 covers the rest.
func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return 1
}
