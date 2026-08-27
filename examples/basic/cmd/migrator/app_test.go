package main

import (
	"context"
	"testing"

	"github.com/okian/servo/v3/servo"
	"github.com/okian/servo/v3/servotest"
)

// This graph has no Runner at all — Migrator only implements Initializer,
// so its work happens during New and Run has nothing to do. Same main.go
// shape as cmd/basic regardless.
func TestAppRunsAndStopsCleanly(t *testing.T) {
	defer servotest.NoLeaks(t)

	app, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	foundMigrator := false
	for _, n := range app.Report().Nodes {
		if n.Type == "*example.com/servobasic/migrator.Migrator" {
			foundMigrator = true
		}
	}
	if !foundMigrator {
		t.Errorf("startup report %v missing the migrator node", app.Report())
	}

	report := app.Shutdown(context.Background())
	if !report.Clean() {
		t.Fatalf("Shutdown not clean: %v", report)
	}

	rec := servotest.NewRecorder(app.Report(), report)
	servotest.AssertStopOrder(t, rec, "*example.com/servobasic/postgres.DB", "*example.com/servobasic/logger.Logger")
}

func TestAppHealthReflectsDB(t *testing.T) {
	app, err := New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer app.Shutdown(context.Background())

	health := app.Health(context.Background())
	if !health.Clean() {
		t.Errorf("Health not clean: %v", health)
	}
	if len(health.Nodes) != 1 || health.Nodes[0].Status != servo.StatusOK {
		t.Errorf("Health = %v, want a single OK node for postgres.DB", health)
	}
}
