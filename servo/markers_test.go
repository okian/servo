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
