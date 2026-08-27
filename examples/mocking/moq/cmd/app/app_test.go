package main

import (
	"context"
	"testing"

	"github.com/okian/servo/v3/servotest"
)

func TestServerLookupUsesMoqMock(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	app.storeMock.GetFunc = func(key string) string { return "mocked:" + key }

	got := app.server.Lookup("user:42")
	if want := "mocked:user:42"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}

	calls := app.storeMock.GetCalls()
	if len(calls) != 1 || calls[0].Key != "user:42" {
		t.Errorf("GetCalls() = %v, want exactly one call with Key user:42", calls)
	}

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
