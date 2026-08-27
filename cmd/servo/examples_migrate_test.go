package main

import (
	"strings"
	"testing"
)

// Pins examples/migrate's committed fixture to the exact report and
// skeleton its README claims servo migrate produces, so the two can't
// drift apart as migrate's output evolves.
func TestExampleMigrateFixture(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runMigrate("../../examples/migrate"); err != nil {
			t.Fatalf("runMigrate: %v", err)
		}
	})

	for _, want := range []string{
		"order=1    Logger",
		"order=2    DB",
		"order=2    Cache",
		"order=3    Server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q, got:\n%s", want, out)
		}
	}

	if strings.Count(out, "shares this order with another service") != 2 {
		t.Errorf("want exactly 2 duplicate-order warnings (DB and Cache sharing order=2), got:\n%s", out)
	}

	for _, want := range []string{
		"servo.Root[*Logger](), // was order=1",
		"servo.Root[*DB](), // was order=2",
		"servo.Root[*Cache](), // was order=2",
		"servo.Root[*Server](), // was order=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("skeleton missing %q, got:\n%s", want, out)
		}
	}
}
