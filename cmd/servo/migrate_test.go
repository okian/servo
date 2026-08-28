package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrateReportsRegistrationsAndDuplicateOrders(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "app.go", `package app

import "example.com/legacy/svc"

func setup() {
	Register(&ServiceA{}, 1)
	Register(&ServiceB{}, 2)
	Register(svc.ServiceC{}, 2)
	NotRegister(&Ignored{}, 1)
	Register(&WrongArgCount{})
	Register(&BadOrder{}, someConst)
}
`)
	// A malformed sibling file must be skipped, not abort the whole walk.
	mustWriteFile(t, dir, "broken.go", "not valid go source {{{")

	out := captureStdout(t, func() {
		if err := runMigrate(dir); err != nil {
			t.Fatalf("runMigrate: %v", err)
		}
	})

	for _, want := range []string{
		"order=1", "ServiceA",
		"order=2", "ServiceB", "svc.ServiceC",
		"shares this order with another service",
		"servo.Root[*ServiceA]()",
		"servo.Root[*svc.ServiceC]()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("migrate report missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Ignored") {
		t.Errorf("NotRegister(...) must not be picked up as a v1 registration, got:\n%s", out)
	}
	if strings.Contains(out, "WrongArgCount") {
		t.Errorf("a Register(...) call with the wrong argument count must be ignored, got:\n%s", out)
	}
	if strings.Contains(out, "BadOrder") {
		t.Errorf("a Register(...) call whose order isn't an int literal must be ignored, got:\n%s", out)
	}
}

func TestRunMigrateNoRegistrationsFound(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "app.go", "package app\n\nfunc setup() {}\n")

	out := captureStdout(t, func() {
		if err := runMigrate(dir); err != nil {
			t.Fatalf("runMigrate: %v", err)
		}
	})
	if !strings.Contains(out, "no v1 Register(...) calls found") {
		t.Errorf("expected a 'no v1 Register(...) calls found' message, got:\n%s", out)
	}
}

// TestRunMigrateFailsWhenDirDoesNotExist covers findV1Registrations'
// filepath.WalkDir error branch: WalkDir invokes its callback once for the
// root itself with a non-nil err when it can't even be lstat'd, and the
// callback propagates that immediately rather than treating it like an
// unparseable file (which is deliberately swallowed).
func TestRunMigrateFailsWhenDirDoesNotExist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if err := runMigrate(dir); err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

// TestRunMigrateIgnoresRegistrationWithNonDecimalOrderLiteral covers
// findV1Registrations' strconv.Atoi failure branch: a hex literal like 0x2
// is a legal Go token.INT (so the *ast.BasicLit/token.INT type check
// passes), but strconv.Atoi doesn't understand Go's 0x/0o/0b prefixes or
// digit-separating underscores, so it fails to parse — the registration
// must be skipped, the same as one with the wrong argument count.
func TestRunMigrateIgnoresRegistrationWithNonDecimalOrderLiteral(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "app.go", `package app

func setup() {
	Register(&HexOrder{}, 0x2)
}
`)
	out := captureStdout(t, func() {
		if err := runMigrate(dir); err != nil {
			t.Fatalf("runMigrate: %v", err)
		}
	})
	if strings.Contains(out, "HexOrder") {
		t.Errorf("a Register(...) call whose order literal strconv.Atoi can't parse must be ignored, got:\n%s", out)
	}
}
