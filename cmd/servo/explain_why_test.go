package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExplainKnownNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/explain", true, "")

	out := captureStdout(t, func() {
		if err := runExplain(cfg(dir), "postgres.DB", false); err != nil {
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
		if err := runExplain(cfg(dir), "api.Server", true); err != nil {
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
	err := runExplain(cfg(dir), "nope.Nothing", false)
	if err == nil || !strings.Contains(err.Error(), "no node matches") {
		t.Fatalf("got err=%v, want a 'no node matches' error", err)
	}
}

func TestRunExplainFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runExplain(cfg(filepath.Join(t.TempDir(), "does-not-exist")), "api.Server", false)
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

func TestRunExplainFailsWhenResolutionFails(t *testing.T) {
	dir := writeAppModule(t, "example.com/explainresolvefail", false, "")
	err := runExplain(cfg(dir), "api.Server", false)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}

func TestRunWhyFromDeeperNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/why", true, "")

	// worker.Consumer depends directly on logger.Logger (2 hops), which is
	// shorter than api.Server -> postgres.DB -> logger.Logger (3 hops), so
	// the shortest path must go through Consumer.
	out := captureStdout(t, func() {
		if err := runWhy(cfg(dir), "logger.Logger", false); err != nil {
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
		if err := runWhy(cfg(dir), "api.Server", false); err != nil {
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
		if err := runWhy(cfg(dir), "logger.Logger", true); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "logger.Logger") || !strings.Contains(out, "[") {
		t.Errorf("expected a JSON array containing logger.Logger, got:\n%s", out)
	}
}

func TestRunWhyUnknownNode(t *testing.T) {
	dir := writeAppModule(t, "example.com/whymissing", true, "")
	err := runWhy(cfg(dir), "nope.Nothing", false)
	if err == nil || !strings.Contains(err.Error(), "no node matches") {
		t.Fatalf("got err=%v, want a 'no node matches' error", err)
	}
}

func TestRunWhyFailsWhenModuleFailsToLoad(t *testing.T) {
	err := runWhy(cfg(filepath.Join(t.TempDir(), "does-not-exist")), "api.Server", false)
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory")
	}
}

func TestRunWhyFailsWhenResolutionFails(t *testing.T) {
	dir := writeAppModule(t, "example.com/whyresolvefail", false, "")
	err := runWhy(cfg(dir), "api.Server", false)
	if err == nil || !strings.Contains(err.Error(), "no provider for") {
		t.Fatalf("got err=%v, want a 'no provider for' ambiguity diagnostic", err)
	}
}

// TestRunWhyDeduplicatesRepeatedRootDeclaration covers
// shortestPathFromRoot's BFS-seed dedup guard: nothing in load/spec.go
// rejects the same servo.Root[T]() type declared twice (unlike Bind/
// Override, which do check), so resolved.Roots can legitimately contain
// the same *Node twice. Without the dedup, the second occurrence would be
// re-queued and re-visited redundantly.
func TestRunWhyDeduplicatesRepeatedRootDeclaration(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/duproot\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/duproot/api"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)

	out := captureStdout(t, func() {
		if err := runWhy(cfg(dir), "api.Server", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "root") || !strings.Contains(out, "api.Server") {
		t.Errorf("expected a trivial root path despite the duplicate Root declaration, got:\n%s", out)
	}
}
