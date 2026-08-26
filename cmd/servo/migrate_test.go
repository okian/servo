package main

import (
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
