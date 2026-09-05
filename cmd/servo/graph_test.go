package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	// Drain the read end concurrently, before fn runs. An OS pipe has a
	// fixed buffer (~64 KB on macOS), so a command that prints more than
	// that — `servo list --all` dumps the whole transitive candidate index
	// — would block in Write forever if nothing were reading yet. Copying
	// on another goroutine keeps the writer unblocked no matter the volume.
	copied := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		copied <- buf.Bytes()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return string(<-copied)
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

// captureStdout must not deadlock when fn writes more than the OS pipe
// buffer (~64 KB on macOS). It used to drain the pipe only after fn
// returned, so a large writer blocked forever with no reader — which is
// exactly what `servo list --all` does once the candidate index crosses
// that size. The -timeout in a plain `go test` turns the deadlock into a
// panic; here a bounded goroutine turns it into a clean failure.
func TestCaptureStdoutHandlesLargeOutput(t *testing.T) {
	const line = "candidate-name                 path/to/file.go:123:4\n"
	const n = 4000 // ~208 KB, comfortably past any pipe buffer

	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() {
			for range n {
				fmt.Print(line)
			}
		})
	}()

	select {
	case got := <-done:
		if want := len(line) * n; len(got) != want {
			t.Fatalf("captured %d bytes, want %d", len(got), want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("captureStdout deadlocked on output larger than the pipe buffer")
	}
}
