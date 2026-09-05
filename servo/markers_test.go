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

func TestHTTPPanics(t *testing.T) {
	expectPanic(t, "servo: HTTP executed at runtime", func() { HTTP() })
}

func TestGroupPanics(t *testing.T) {
	expectPanic(t, "servo: Group executed at runtime", func() { Group("telemetry") })
}

func TestUsePanics(t *testing.T) {
	expectPanic(t, "servo: Use executed at runtime", func() { Use[int]() })
}

func TestRoutePanics(t *testing.T) {
	expectPanic(t, "servo: Route executed at runtime", func() { Route("GET /x") })
}

func TestExtractPanics(t *testing.T) {
	expectPanic(t, "servo: Extract executed at runtime", func() { Extract[int]() })
}
