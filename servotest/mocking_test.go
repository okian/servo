package servotest

import (
	"fmt"
	"testing"
)

func expectPanicContaining(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected a panic containing %q, got none", want)
		}
		if got := fmt.Sprint(r); got != want {
			t.Errorf("panic value = %q, want %q", got, want)
		}
	}()
	fn()
}

func TestPanicReporterErrorfPanicsWithFormattedMessage(t *testing.T) {
	expectPanicContaining(t, "boom 42", func() {
		PanicReporter{}.Errorf("boom %d", 42)
	})
}

func TestPanicReporterFatalfPanicsWithFormattedMessage(t *testing.T) {
	expectPanicContaining(t, "fatal: disk full", func() {
		PanicReporter{}.Fatalf("fatal: %s", "disk full")
	})
}
