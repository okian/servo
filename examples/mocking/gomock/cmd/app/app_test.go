package main

import (
	"context"
	"testing"

	"github.com/okian/servo/v2/servotest"
)

func TestServerLookupUsesGomockMock(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	// Finish must be called directly by the test (a plain defer), right
	// after construction — never routed through servo's (T, func())
	// cleanup shape, which would run it inside servo.RunStop's own
	// goroutine during Shutdown and panic there instead of here.
	defer app.mockStoreForServo.Finish()

	app.mockStoreForServo.EXPECT().Get("user:42").Return("mocked-value")

	got := app.server.Lookup("user:42")
	if want := "mocked-value"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
