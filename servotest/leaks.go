// Package servotest provides test helpers for generated injectors:
// goroutine-leak checking, ordering assertions read directly from a
// generated App's own reports, and a way to shrink stop budgets to
// exercise the abandoned-node path without a slow suite.
package servotest

import (
	"testing"

	"go.uber.org/goleak"
)

// NoLeaks fails t if any goroutine started during the test is still
// running when it returns. Used by servo's own suite and exported for
// generated-app tests.
func NoLeaks(t *testing.T) {
	t.Helper()
	goleak.VerifyNone(t)
}
