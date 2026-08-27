package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
	"github.com/okian/servo/v3/servotest"
)

func TestAppStartsAndStopsCleanly(t *testing.T) {
	defer servotest.NoLeaks(t)

	ctx, cancel := context.WithCancel(context.Background())
	app, err := NewTestApp(ctx)
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	report := app.Shutdown(context.Background())
	if !report.Clean() {
		t.Fatalf("Shutdown not clean: %v", report)
	}

	rec := servotest.NewRecorder(app.Report(), report)
	servotest.AssertStopOrder(t, rec, "*example.com/servobasic/logger.Logger", "*example.com/servobasic/api.Server")
}

// TestServerLookupUsesMockStore is the classic mock pattern: configure the
// mock's return value, exercise real code that depends on the interface,
// then assert on what the mock recorded — no real postgres involved at
// all, and NewTestApp/TestApp exist specifically so this doesn't require
// hand-wiring anything.
func TestServerLookupUsesMockStore(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	app.store.Return = "mocked-value"

	got := app.server.Lookup("user:42")
	if got != "mocked-value" {
		t.Errorf("Lookup(%q) = %q, want %q", "user:42", got, "mocked-value")
	}
	if want := []string{"user:42"}; len(app.store.Gets) != 1 || app.store.Gets[0] != want[0] {
		t.Errorf("mock recorded %v, want %v", app.store.Gets, want)
	}

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("Shutdown not clean: %v", r)
	}
}

// TestShutdownIsSafeToCallTwice covers a second Shutdown call on an
// already-shut-down App. Each node's own stop method is idempotent
// (sync.Once), but the outer Shutdown body has no guard of its own —
// every call registers its own signal watcher goroutine — so this checks
// the second call is still clean and, via NoLeaks, that the first call's
// watcher goroutine actually exited rather than piling up.
func TestShutdownIsSafeToCallTwice(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	first := app.Shutdown(context.Background())
	if !first.Clean() {
		t.Fatalf("first Shutdown not clean: %v", first)
	}
	second := app.Shutdown(context.Background())
	if !second.Clean() {
		t.Fatalf("second Shutdown not clean: %v", second)
	}
}

// TestShutdownIsSafeToCallConcurrently covers two goroutines calling
// Shutdown on the same App at once. sync.Once guarantees each node's stop
// logic runs exactly once and that every caller observes the same result
// only after it completes, so both concurrent calls must report clean.
func TestShutdownIsSafeToCallConcurrently(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	var wg sync.WaitGroup
	reports := make([]servo.Report, 2)
	for i := range reports {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reports[i] = app.Shutdown(context.Background())
		}(i)
	}
	wg.Wait()

	for i, r := range reports {
		if !r.Clean() {
			t.Errorf("concurrent Shutdown[%d] not clean: %v", i, r)
		}
	}
}

// TestShutdownReportsHungStoreAsAbandoned exercises the case where a
// component ignores cancellation: it must be reported abandoned, never
// claimed clean. It uses servotest.Timeout to shrink the budget so the
// test takes milliseconds instead of the real 5s default. NoLeaks is
// deliberately not used here: HangOnStop simulates a component that never
// returns, which is a genuine, intentional leak for this one test, not a
// bug.
func TestShutdownReportsHungStoreAsAbandoned(t *testing.T) {
	servotest.Timeout(t, 20*time.Millisecond)

	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	app.store.HangOnStop = true

	start := time.Now()
	report := app.Shutdown(context.Background())
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown took %v, want it bounded by the shrunk budget (~20ms), not the real default", elapsed)
	}
	if report.Clean() {
		t.Fatal("expected the hung store to make the report non-clean")
	}

	var storeResult *servo.NodeResult
	for i := range report.Nodes {
		if report.Nodes[i].Name == "*example.com/servobasic/mockstore.Store" {
			storeResult = &report.Nodes[i]
		}
	}
	if storeResult == nil {
		t.Fatal("mockstore.Store missing from the shutdown report")
	}
	if storeResult.Status != servo.StatusAbandoned {
		t.Errorf("mockstore.Store status = %v, want %v (abandoned, not silently clean)", storeResult.Status, servo.StatusAbandoned)
	}
}
