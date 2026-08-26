package servotest

import (
	"testing"
	"time"

	"github.com/okian/servo/v2/servo"
)

// Timeout overrides servo.DefaultStopBudget for the calling test, restoring
// it via t.Cleanup, so a short budget exercises the abandoned-node path
// without a slow suite. Since the budget is a package var, tests using
// Timeout must not run in parallel with each other or with tests that
// depend on the real default.
func Timeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := servo.DefaultStopBudget
	servo.DefaultStopBudget = d
	t.Cleanup(func() { servo.DefaultStopBudget = prev })
}
