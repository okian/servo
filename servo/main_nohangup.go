//go:build js || plan9

package servo

import "os"

// js/wasm has no SIGHUP to subscribe to, and plan9 numbers no signals, so
// on those two ports Main watches SIGINT and SIGTERM alone and a second
// signal exits 1. Neither delivers a signal to a Go program in practice;
// this file exists so package servo keeps compiling there.
var hangupSignals []os.Signal

func signalExitCode(os.Signal) int { return 1 }
