package servo

import (
	"strings"
	"testing"
)

func expectPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected a panic containing %q, got none", want)
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected a string panic value, got %T: %v", r, r)
		}
		if !strings.Contains(msg, want) {
			t.Fatalf("panic message = %q, want it to contain %q", msg, want)
		}
	}()
	fn()
}

func TestBuildPanics(t *testing.T) {
	expectPanic(t, "servo: Build executed at runtime", func() { Build() })
}

func TestRootPanics(t *testing.T) {
	expectPanic(t, "servo: Root executed at runtime", func() { Root[int]() })
}

func TestBindPanics(t *testing.T) {
	expectPanic(t, "servo: Bind executed at runtime", func() { Bind[int, int]() })
}

func TestOverridePanics(t *testing.T) {
	expectPanic(t, "servo: Override executed at runtime", func() { Override[int, int]() })
}

// TestValuePanicsAndSaysHowToFixIt asserts the whole message, not just the
// prefix the older markers check. A marker only ever executes because the
// servoinject build tag went missing from the spec file, and this panic is
// the entire diagnostic the reader gets for that — it has to name which
// marker ran and what to do about it, or it is a stack trace pointing at a
// one-line function with no explanation.
func TestValuePanicsAndSaysHowToFixIt(t *testing.T) {
	expectPanic(t, "servo: Value executed at runtime — run `servo generate`", func() { Value[int]() })
}

// TestIncludePanicsWithoutCallingTheFunctionItWasGiven pins the half of
// Include's contract that is easy to lose: the argument names a function
// whose returned slice `servo generate` reads as syntax, so Include must
// never invoke it. A future implementation that expanded the markers at
// runtime before panicking would run arbitrary user code from a function
// the author was promised is never called.
func TestIncludePanicsWithoutCallingTheFunctionItWasGiven(t *testing.T) {
	called := false
	shared := func() []Marker {
		called = true
		return nil
	}

	expectPanic(t, "servo: Include executed at runtime — run `servo generate`", func() { Include(shared) })

	if called {
		t.Fatalf("Include invoked the function it was given; generate reads that declaration as syntax and must never execute it")
	}
}
