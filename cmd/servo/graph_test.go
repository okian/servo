package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. Several commands (runGraph, runList, runNew,
// runMigrate, runInit, runDoctor, runExplain, runWhy) print directly to
// os.Stdout via fmt.Print*, rather than accepting a Writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRunGraphAllFormats(t *testing.T) {
	dir := writeAppModule(t, "example.com/graphfmt", true, "")

	cases := []struct {
		format string
		want   string
	}{
		{"", "Level 1"},
		{"text", "Level 1"},
		{"json", `"nodes"`},
		{"dot", "digraph servo {"},
		{"mermaid", "graph BT"},
	}
	for _, c := range cases {
		out := captureStdout(t, func() {
			if err := runGraph(cfg(dir), c.format); err != nil {
				t.Fatalf("runGraph(cfg(%q)): %v", c.format, err)
			}
		})
		if !strings.Contains(out, c.want) {
			t.Errorf("format %q: output missing %q, got:\n%s", c.format, c.want, out)
		}
	}
}

func TestRunGraphUnknownFormat(t *testing.T) {
	dir := writeAppModule(t, "example.com/graphbad", true, "")
	err := runGraph(cfg(dir), "yaml")
	if err == nil || !strings.Contains(err.Error(), "unknown --format") {
		t.Fatalf("got err=%v, want an 'unknown --format' error", err)
	}
}

func TestRunGraphFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runGraph(cfg(filepath.Join(t.TempDir(), "does-not-exist")), "text")
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

func TestRunGraphFailsWhenResolutionFails(t *testing.T) {
	dir := writeAppModule(t, "example.com/graphresolvefail", false, "")
	err := runGraph(cfg(dir), "text")
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}
