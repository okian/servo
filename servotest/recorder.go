package servotest

import (
	"testing"

	"github.com/okian/servo/v2/servo"
)

// Recorder wraps a generated App's own init and shutdown reports so
// ordering guarantees can be asserted directly against them — no separate
// instrumentation hooks in generated code, since Shutdown's Report already
// lists nodes in the order they were actually stopped, and New's
// StartupReport already lists nodes in the order they were actually
// initialized.
type Recorder struct {
	Init     servo.StartupReport
	Shutdown servo.Report
}

// NewRecorder builds a Recorder from an app's init and shutdown reports.
// Call app.Report() right after New/NewTestApp succeeds, and app.Shutdown
// after driving the app through its test.
func NewRecorder(init servo.StartupReport, shutdown servo.Report) *Recorder {
	return &Recorder{Init: init, Shutdown: shutdown}
}

// AssertStopOrder fails t unless want appears, in order, as a subsequence
// of the nodes Shutdown actually stopped (other nodes may be interspersed
// — this asserts a relative guarantee, not an exact full sequence).
func AssertStopOrder(t *testing.T, rec *Recorder, want ...string) {
	t.Helper()
	got := make([]string, len(rec.Shutdown.Nodes))
	for i, n := range rec.Shutdown.Nodes {
		got[i] = n.Name
	}
	assertSubsequence(t, "stop", got, want)
}

// AssertInitOrder fails t unless want appears, in order, as a subsequence
// of the nodes actually initialized.
func AssertInitOrder(t *testing.T, rec *Recorder, want ...string) {
	t.Helper()
	got := make([]string, len(rec.Init.Nodes))
	for i, n := range rec.Init.Nodes {
		got[i] = n.Type
	}
	assertSubsequence(t, "init", got, want)
}

func assertSubsequence(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !isSubsequence(got, want) {
		t.Fatalf("%s order %v does not contain %v as a subsequence", label, got, want)
	}
}

// isSubsequence reports whether want appears, in order, within got (other
// elements may be interspersed).
func isSubsequence(got, want []string) bool {
	idx := 0
	for _, g := range got {
		if idx < len(want) && g == want[idx] {
			idx++
		}
	}
	return idx == len(want)
}
