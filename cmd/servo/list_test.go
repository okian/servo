package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunListDefaultShowsInScopeCandidates(t *testing.T) {
	dir := writeAppModule(t, "example.com/list", true, "")

	out := captureStdout(t, func() {
		if err := runList(cfg(dir), false, false, false); err != nil {
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
		if err := runList(cfg(dir), true, false, false); err != nil {
			t.Fatalf("runList --rejected: %v", err)
		}
	})
	if !strings.Contains(out, "api.NewID") || !strings.Contains(out, "primitive") {
		t.Errorf("expected api.NewID rejected as a primitive result, got:\n%s", out)
	}
}

// The rejected listing answers "why is nothing providing T?": each refused
// function names what it would have provided, and the methods — every
// Init, Stop and Health in the module, most of the output on a real
// project — are grouped by receiver after the functions instead of
// drowning them.
func TestRunListRejectedGroupsMethodsAndNamesTypes(t *testing.T) {
	dir := writeAppModule(t, "example.com/listgrouped", true, "")

	out := captureStdout(t, func() {
		if err := runList(cfg(dir), true, false, false); err != nil {
			t.Fatalf("runList --rejected: %v", err)
		}
	})
	idLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "api.NewID ") {
			idLine = line
		}
	}
	if !strings.Contains(idLine, " string ") || !strings.Contains(idLine, "result type is a primitive (string)") {
		t.Errorf("api.NewID's line should name the type it would provide, got %q in:\n%s", idLine, out)
	}
	methods := strings.Index(out, "methods are never providers")
	if methods < 0 || methods < strings.Index(out, "api.NewID") {
		t.Fatalf("methods should follow the functions under their own heading, got:\n%s", out)
	}
	if !strings.Contains(out[methods:], "api.(*Server)") || !strings.Contains(out[methods:], "Run, Ready") {
		t.Errorf("methods should be grouped by receiver, got:\n%s", out[methods:])
	}
	if strings.Contains(out, "method, not a function") {
		t.Errorf("the text listing should not repeat the method reason per line, got:\n%s", out)
	}

	jsonOut := captureStdout(t, func() {
		if err := runList(cfg(dir), true, false, true); err != nil {
			t.Fatalf("runList --rejected --json: %v", err)
		}
	})
	for _, want := range []string{`"kind": "function"`, `"kind": "method"`, `"provides": "string"`} {
		if !strings.Contains(jsonOut, want) {
			t.Errorf("JSON output missing %s:\n%s", want, jsonOut)
		}
	}
}

// A candidate the injector cannot refer to — here an exported constructor
// returning an unexported type — is in the index but never selected, so
// the rejected listing names it, and why, under its own heading.
func TestRunListRejectedShowsUnusableCandidates(t *testing.T) {
	dir := writeAppModule(t, "example.com/listunusable", true, "")
	mustWriteFile(t, dir, "comp/comp.go", "package comp\n\ntype pool struct{}\n\nfunc NewPool() *pool { return &pool{} }\n")

	out := captureStdout(t, func() {
		if err := runList(cfg(dir), true, false, false); err != nil {
			t.Fatalf("runList --rejected: %v", err)
		}
	})
	heading := strings.Index(out, "candidates example.com/listunusable/cmd/app cannot use:")
	if heading < 0 {
		t.Fatalf("no unusable-candidates section, got:\n%s", out)
	}
	if section := out[heading:]; !strings.Contains(section, "comp.NewPool") || !strings.Contains(section, "*comp.pool") || !strings.Contains(section, "is unexported") {
		t.Errorf("comp.NewPool should be listed with its type and reason, got:\n%s", section)
	}
}

func TestRunListJSONVariants(t *testing.T) {
	dir := writeAppModule(t, "example.com/listjson", true, "")

	accepted := captureStdout(t, func() {
		if err := runList(cfg(dir), false, false, true); err != nil {
			t.Fatalf("runList --json: %v", err)
		}
	})
	if !strings.Contains(accepted, `"name"`) || !strings.Contains(accepted, "api.New") {
		t.Errorf("expected JSON accepted output, got:\n%s", accepted)
	}

	rejected := captureStdout(t, func() {
		if err := runList(cfg(dir), true, false, true); err != nil {
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
		if err := runList(cfg(dir), false, false, false); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	all := captureStdout(t, func() {
		if err := runList(cfg(dir), false, true, false); err != nil {
			t.Fatalf("runList --all: %v", err)
		}
	})

	countLines := func(s string) int { return len(strings.Split(strings.TrimRight(s, "\n"), "\n")) }
	if countLines(all) <= countLines(inScope) {
		t.Errorf("--all (%d lines) should include strictly more candidates than the default main-module scope (%d lines)", countLines(all), countLines(inScope))
	}
}

func TestRunListFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runList(cfg(filepath.Join(t.TempDir(), "does-not-exist")), false, false, false)
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

// TestRunListRejectedJSONIncludesUnusableCandidates: the JSON form of
// the rejected listing carries the candidates the injector cannot use as
// their own kind, with the type each would have provided.
func TestRunListRejectedJSONIncludesUnusableCandidates(t *testing.T) {
	dir := writeAppModule(t, "example.com/listunusablejson", true, "")
	mustWriteFile(t, dir, "comp/comp.go", "package comp\n\ntype pool struct{}\n\nfunc NewPool() *pool { return &pool{} }\n")

	out := captureStdout(t, func() {
		if err := runList(cfg(dir), true, false, true); err != nil {
			t.Fatalf("runList --rejected --json: %v", err)
		}
	})
	for _, want := range []string{`"name": "comp.NewPool"`, `"provides": "*comp.pool"`, `"kind": "unusable"`} {
		if !strings.Contains(out, want) {
			t.Errorf("list --rejected --json missing %s:\n%s", want, out)
		}
	}
}
