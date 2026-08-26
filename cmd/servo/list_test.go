package main

import (
	"strings"
	"testing"
)

func TestRunListDefaultShowsInScopeCandidates(t *testing.T) {
	dir := writeAppModule(t, "example.com/list", true, "")

	out := captureStdout(t, func() {
		if err := runList(dir, false, false, false); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	for _, want := range []string{"api.New", "logger.New", "postgres.New", "worker.New", "memory.New"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "api.NewID") {
		t.Errorf("rejected candidate api.NewID must not appear in the accepted list, got:\n%s", out)
	}
}

func TestRunListRejectedShowsExcludedFunctions(t *testing.T) {
	dir := writeAppModule(t, "example.com/listrejected", true, "")

	out := captureStdout(t, func() {
		if err := runList(dir, true, false, false); err != nil {
			t.Fatalf("runList --rejected: %v", err)
		}
	})
	if !strings.Contains(out, "api.NewID") || !strings.Contains(out, "primitive") {
		t.Errorf("expected api.NewID rejected as a primitive result, got:\n%s", out)
	}
}

func TestRunListJSONVariants(t *testing.T) {
	dir := writeAppModule(t, "example.com/listjson", true, "")

	accepted := captureStdout(t, func() {
		if err := runList(dir, false, false, true); err != nil {
			t.Fatalf("runList --json: %v", err)
		}
	})
	if !strings.Contains(accepted, `"name"`) || !strings.Contains(accepted, "api.New") {
		t.Errorf("expected JSON accepted output, got:\n%s", accepted)
	}

	rejected := captureStdout(t, func() {
		if err := runList(dir, true, false, true); err != nil {
			t.Fatalf("runList --rejected --json: %v", err)
		}
	})
	if !strings.Contains(rejected, `"reason"`) || !strings.Contains(rejected, "api.NewID") {
		t.Errorf("expected JSON rejected output, got:\n%s", rejected)
	}
}

func TestRunListAllIncludesOutOfScopeCandidates(t *testing.T) {
	dir := writeAppModule(t, "example.com/listall", true, "")

	inScope := captureStdout(t, func() {
		if err := runList(dir, false, false, false); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	all := captureStdout(t, func() {
		if err := runList(dir, false, true, false); err != nil {
			t.Fatalf("runList --all: %v", err)
		}
	})

	countLines := func(s string) int { return len(strings.Split(strings.TrimRight(s, "\n"), "\n")) }
	if countLines(all) <= countLines(inScope) {
		t.Errorf("--all (%d lines) should include strictly more candidates than the default main-module scope (%d lines)", countLines(all), countLines(inScope))
	}
}
