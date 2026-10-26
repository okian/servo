package servotest

import (
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

// Timeout overrides servo.DefaultStopBudget for the calling test, restoring
// it via t.Cleanup, so a short budget exercises the abandoned-node path
// without a slow suite.
//
// The budget is a package variable, so a test changing it must not run
// beside one reading it — that is a data race, and a flake. Timeout makes
// Go enforce it rather than leaving it to a comment: it calls t.Setenv
// (setting SERVOTEST_TIMEOUT, which nothing reads) before touching the
// budget, and the testing package refuses t.Setenv in a test that has
// called t.Parallel, or whose parent has, and refuses t.Parallel after
// it — both with a panic naming the conflict.
func Timeout(t *testing.T, d time.Duration) {
	t.Helper()
	t.Setenv("SERVOTEST_TIMEOUT", d.String())
	prev := servo.DefaultStopBudget
	servo.DefaultStopBudget = d
	t.Cleanup(func() { servo.DefaultStopBudget = prev })
}
