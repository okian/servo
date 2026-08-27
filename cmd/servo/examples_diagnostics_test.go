package main

import (
	"strings"
	"testing"
)

// These pin examples/diagnostics/*'s committed fixtures to the exact
// diagnostic each one's README claims it produces, so the two can't drift
// apart as resolve's diagnostic wording evolves.

func TestExampleDiagnosticMissingProvider(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/missing")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no provider for example.com/servodiagnostics/missing.Store") {
		t.Errorf("got %v, want a missing-provider diagnostic for Store", err)
	}
	if strings.Contains(msg, "types implement") {
		t.Errorf("got %v, want no candidate list — nothing implements Store here", err)
	}
}

func TestExampleDiagnosticAmbiguousProvider(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/ambiguous")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no provider for example.com/servodiagnostics/ambiguous.Store") {
		t.Errorf("got %v, want a missing-provider diagnostic for Store", err)
	}
	if !strings.Contains(msg, "2 types implement example.com/servodiagnostics/ambiguous.Store") {
		t.Errorf("got %v, want a 2-candidate ambiguity list", err)
	}
	if !strings.Contains(msg, "Postgres") || !strings.Contains(msg, "Redis") {
		t.Errorf("got %v, want both Postgres and Redis suggested", err)
	}
}

func TestExampleDiagnosticCycle(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/cycle")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "servo: dependency cycle detected") {
		t.Errorf("got %v, want a cycle diagnostic", err)
	}
	if !strings.Contains(msg, "cycle.A") || !strings.Contains(msg, "cycle.B") {
		t.Errorf("got %v, want both A and B named in the cycle", err)
	}
}
