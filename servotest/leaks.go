// Package servotest provides test helpers for generated injectors:
// goroutine-leak checking, ordering assertions read directly from a
// generated App's own reports, and a way to shrink stop budgets to
// exercise the abandoned-node path without a slow suite.
package servotest

import (
	"testing"

	"go.uber.org/goleak"
)

// NoLeaks fails t if any goroutine is running when it returns, beyond the
// ones goleak's own default ignore list covers.
//
// Note "any", not "any this test started": goleak has no notion of when a
// goroutine appeared, so a goroutine left behind by an *earlier* test in
// the same binary is reported against whichever test calls NoLeaks next.
// That is the right check for a package whose tests all clean up after
// themselves, and it is what servo's own suite uses.
//
// It is the wrong check for a package that also has a test which leaves a
// goroutine running on purpose — the abandoned-node path Timeout exists to
// exercise does exactly that, since the node being abandoned is by
// definition one that never returns. Use NoNewLeaks there.
func NoLeaks(t *testing.T) {
	t.Helper()
	goleak.VerifyNone(t)
}

// NoNewLeaks records the goroutines already running and returns a check for
// the ones this test adds on top of them:
//
//	defer servotest.NoNewLeaks(t)()
//
// The call shape is what makes it correct. The baseline has to be taken
// before the test body runs, and the check after — so NoNewLeaks is called
// at the top and the function it returns is deferred, rather than the whole
// thing being deferred.
//
// `defer servotest.NoNewLeaks(t)` — no trailing parentheses — compiles,
// runs, and checks nothing. Nothing catches it either: go vet's unusedresult
// analyzer only inspects a fixed list of standard-library functions, so the
// discarded result is silent. It is the one way to hold this wrong, and the
// symptom is a leak check that always passes.
//
// Use it in any package where another test parks a goroutine deliberately.
// Prefer NoLeaks where nothing does: it also catches a leak inherited from
// a sibling test, which is a real defect that a baseline hides.
func NoNewLeaks(t *testing.T) func() {
	t.Helper()
	baseline := goleak.IgnoreCurrent()
	return func() {
		t.Helper()
		goleak.VerifyNone(t, baseline)
	}
}
