package main

import (
	"strings"
	"testing"
)

func TestRunExplainKnownNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/explain", true, "")

	out := captureStdout(t, func() {
		if err := runExplain(dir, "postgres.DB", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{
		"postgres.DB",
		"binding:      explicit bind",
		"level:        2",
		"depends on:   ",
		"logger.Logger",
		"depended on:  ",
		"api.Server",
		"Initializer",
		"Finalizer",
		"Healther",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunExplainJSON(t *testing.T) {
	dir := writeAppModule(t, "example.com/explainjson", true, "")

	out := captureStdout(t, func() {
		if err := runExplain(dir, "api.Server", true); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{`"type"`, `"binding"`, "api.Server"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain --json output missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunExplainUnknownNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/explainmissing", true, "")
	err := runExplain(dir, "nope.Nothing", false)
	if err == nil || !strings.Contains(err.Error(), "no node matches") {
		t.Fatalf("got err=%v, want a 'no node matches' error", err)
	}
}

func TestRunWhyFromDeeperNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/why", true, "")

	// worker.Consumer depends directly on logger.Logger (2 hops), which is
	// shorter than api.Server -> postgres.DB -> logger.Logger (3 hops), so
	// the shortest path must go through Consumer.
	out := captureStdout(t, func() {
		if err := runWhy(dir, "logger.Logger", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "root  ") || !strings.Contains(out, "worker.Consumer") {
		t.Errorf("expected the path to start at worker.Consumer, got:\n%s", out)
	}
	if !strings.Contains(out, "-> ") || !strings.Contains(out, "logger.Logger") {
		t.Errorf("expected the path to reach logger.Logger, got:\n%s", out)
	}
	if strings.Contains(out, "api.Server") {
		t.Errorf("expected the shorter Consumer path, not the longer Server path, got:\n%s", out)
	}
}

func TestRunWhyOnARootItself(t *testing.T) {
	dir := writeAppModule(t, "example.com/whyroot", true, "")

	out := captureStdout(t, func() {
		if err := runWhy(dir, "api.Server", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "root") || !strings.Contains(out, "api.Server") {
		t.Errorf("expected a trivial one-node root path, got:\n%s", out)
	}
	if strings.Contains(out, "-> ") {
		t.Errorf("a root's own path must have no '->' continuation lines, got:\n%s", out)
	}
}

func TestRunWhyJSON(t *testing.T) {
	dir := writeAppModule(t, "example.com/whyjson", true, "")

	out := captureStdout(t, func() {
		if err := runWhy(dir, "logger.Logger", true); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "logger.Logger") || !strings.Contains(out, "[") {
		t.Errorf("expected a JSON array containing logger.Logger, got:\n%s", out)
	}
}

func TestRunWhyUnknownNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/whymissing", true, "")
	err := runWhy(dir, "nope.Nothing", false)
	if err == nil || !strings.Contains(err.Error(), "no node matches") {
		t.Fatalf("got err=%v, want a 'no node matches' error", err)
	}
}
