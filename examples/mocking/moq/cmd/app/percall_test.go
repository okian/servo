package main

import (
	"context"
	"testing"

	"example.com/servomocking/moq/mockstore"
)

// TestServerLookupWithAPerCallMoqMock hands NewTestAppWith a moq mock
// configured before construction rather than patched afterwards through
// the App's field, and private to this test, so it can run in parallel.
func TestServerLookupWithAPerCallMoqMock(t *testing.T) {
	t.Parallel()

	mock := &mockstore.StoreMock{GetFunc: func(key string) string { return "per-call:" + key }}

	app, err := NewTestAppWith(context.Background(), TestValues{Store: mock})
	if err != nil {
		t.Fatalf("NewTestAppWith: %v", err)
	}
	if got, want := app.server.Lookup("user:42"), "per-call:user:42"; got != want {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, want)
	}
	if calls := mock.GetCalls(); len(calls) != 1 || calls[0].Key != "user:42" {
		t.Errorf("GetCalls() = %v, want exactly one call with Key user:42", calls)
	}
	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}
