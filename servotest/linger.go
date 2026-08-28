package servotest

import (
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

// Linger overrides every generated scope's declared linger window for the
// calling test, restoring the previous setting via t.Cleanup.
//
// It exists for the same reason Timeout does: the interesting behaviour
// lives at a boundary a real 30-second window cannot be driven to in a
// test. Linger(t, 0) makes an instance evict the moment its last holder
// releases, which is how the eviction-racing-acquire path gets exercised
// deterministically instead of by luck.
//
// Generated code reads the override once per scope, inside New, so call
// this before constructing the app. Since the underlying setting is a
// package var, tests using Linger must not run in parallel with each other
// or with tests that depend on a scope's real declared window.
func Linger(t *testing.T, d time.Duration) {
	t.Helper()
	prev := servo.LingerOverride
	servo.LingerOverride = d
	t.Cleanup(func() { servo.LingerOverride = prev })
}
