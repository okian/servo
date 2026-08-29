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

// The four scope diagnostics get the same treatment: each fixture is
// permanently broken in exactly one way, and its README entry has to keep
// matching what generation actually prints.

func TestExampleDiagnosticWidening(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/widening")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"*example.com/servodiagnostics/widening.Room is scoped, but *example.com/servodiagnostics/widening.Server is a singleton that depends on it",
		"needed by *example.com/servodiagnostics/widening.Server",
		"root",
		"depend on the accessor instead",
		"example.com/servodiagnostics/widening.Rooms",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestExampleDiagnosticCrossScope(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/crossscope")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"are in different scopes",
		"is keyed by example.com/servodiagnostics/crossscope.RoomKey",
		"is keyed by example.com/servodiagnostics/crossscope.TenantKey",
		"Nested scopes are deliberately not supported in this release",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestExampleDiagnosticExtractorCycle(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/extractor")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"ScopeKey extractor depends on *example.com/servodiagnostics/extractor.Decoder, which is itself scoped",
		"runs before",
		"any instance exists",
		// The chain leads to the scoped dependency rather than to the
		// extractor, whose own position is printed on the line above it.
		"needed by *example.com/servodiagnostics/extractor.Decoder",
		"needed by *example.com/servodiagnostics/extractor.Session",
		"root",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestExampleDiagnosticUndeclaredScope(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/undeclared")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"declares a ScopeKey method but no servo.Scoped declares it",
		// Reached mid-traversal, so the chain comes from the DFS path
		// rather than from a walk of a graph that does not exist yet.
		"needed by *example.com/servodiagnostics/undeclared.Tenant",
		"needed by *example.com/servodiagnostics/undeclared.Server",
		"root",
		"type Tenants interface {",
		"Acquire(ctx context.Context) (*Tenant, func(), error)",
		"servo.Scoped[*example.com/servodiagnostics/undeclared.Tenant, example.com/servodiagnostics/undeclared.Tenants](),",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestExampleDiagnosticScopedWithoutScopeKey is the reverse of the
// undeclared-scope fixture: the declaration is there, the method is not.
// It is the half a new user meets first, having reached for servo.Scoped
// before writing the extractor, and until now its wording was pinned only
// by a resolver unit test rather than by a fixture anyone can run.
func TestExampleDiagnosticScopedWithoutScopeKey(t *testing.T) {
	err := runGenerate("../../examples/diagnostics/noscopekey")
	if err == nil {
		t.Fatal("expected generation to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"declares a scope, but *example.com/servodiagnostics/noscopekey.Cache has no ScopeKey method",
		"The receiver must be unnamed",
		"func (*Cache) ScopeKey(ctx context.Context)",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}
