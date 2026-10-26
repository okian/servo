package main

import (
	"context"
	"testing"

	"example.com/servomocking/mockery/mocks"
)

// TestServerLookupWithAPerCallMockeryMock hands NewTestAppWith the mock
// mockery's own constructor builds from the test's t, which registers
// AssertExpectations on t's cleanup — the adapter type is needed only for
// the NewTestApp path that builds the Override itself.
func TestServerLookupWithAPerCallMockeryMock(t *testing.T) {
	t.Parallel()

	mock := mocks.NewStore(t)
	mock.On("Get", "user:42").Return("mocked-value")

	app, err := NewTestAppWith(context.Background(), TestValues{Store: mock})
	if err != nil {
		t.Fatalf("NewTestAppWith: %v", err)
	}
	if got, want := app.server.Lookup("user:42"), "mocked-value"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}
	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
