package main

import (
	"context"
	"testing"

	"go.uber.org/mock/gomock"

	"example.com/servomocking/gomock/mockgenstore"
)

// TestServerLookupWithAPerCallGomockMock hands NewTestAppWith a mock built
// by the test itself, on a controller holding the test's own t: an unmet
// expectation fails this test through t, cleanly, and the controller's
// Finish runs on t's cleanup — no PanicReporter, no Finish to remember.
// Each test gets its own mock, so the test can run in parallel.
func TestServerLookupWithAPerCallGomockMock(t *testing.T) {
	t.Parallel()

	mock := mockgenstore.NewMockStore(gomock.NewController(t))
	mock.EXPECT().Get("user:42").Return("mocked-value")

	app, err := NewTestAppWith(context.Background(), TestValues{Store: mock})
	if err != nil {
		t.Fatalf("NewTestAppWith: %v", err)
	}
	if got, want := app.server.Lookup("user:42"), "mocked-value"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}
	// Supplied, the Override's own constructor never ran.
	if app.mockStoreForServo != nil {
		t.Error("the adapter was built although the test supplied the mock")
	}
	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
