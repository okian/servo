package main

import (
	"context"
	"testing"

	"github.com/okian/servo/v2/servotest"
)

func TestServerLookupUsesMockeryMock(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	app.storeMock.On("Get", "user:42").Return("mocked-value")

	got := app.server.Lookup("user:42")
	if want := "mocked-value"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}

	// testify passes t explicitly at the call site, rather than baking a
	// reporter in at construction time: nothing auto-registered this, call
	// it yourself.
	app.storeMock.AssertExpectations(t)

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
