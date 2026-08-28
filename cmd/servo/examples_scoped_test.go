package main

import (
	"strings"
	"testing"
)

// These pin the scope-aware behaviour of the single-graph commands
// against examples/scoped's committed module. Every other explain/why
// test builds a scope-free module on the fly, so nothing else exercises
// the branches that only exist once a node is one-per-key.

func TestRunExplainScopedNode(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runExplain("../../examples/scoped", "chat.Room", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{
		"*example.com/servoscoped/chat.Room",
		// The line that distinguishes a scoped node from a singleton, and
		// names the policy it was declared with.
		"lifetime:     scoped — one per example.com/servoscoped/chat.RoomKey, linger 30s, max 10000",
		// Its level is counted within the scope, not within the app.
		"level:        2",
		// A scoped node's consumers hold the accessor, not the node, so
		// "depended on: none" would be actively misleading.
		"depended on:  (acquired via example.com/servoscoped/chat.Rooms)",
		"capabilities: Initializer, Runner, Drainer, Finalizer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q, got:\n%s", want, out)
		}
	}
}

// TestRunExplainSingletonSaysSo is the other half: the lifetime line is
// printed for every node, so a singleton has to say what it is rather
// than say nothing.
func TestRunExplainSingletonSaysSo(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runExplain("../../examples/scoped", "api.Server", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	if !strings.Contains(out, "lifetime:     singleton — one per process, built by New") {
		t.Errorf("explain output missing the singleton lifetime, got:\n%s", out)
	}
}

func TestRunExplainScopedJSON(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runExplain("../../examples/scoped", "chat.RoomLog", true); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{
		`"scope": "example.com/servoscoped/chat.RoomKey"`,
		`"lifetime": "scoped`,
		`"level": 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain --json output missing %q, got:\n%s", want, out)
		}
	}
}

// TestRunExplainFindsATransitivelyScopedNode: RoomLog is in the scope
// only because it takes the key, and it is not in Resolved.Order at all —
// so findNode has to look through the scopes to see it.
func TestRunExplainFindsATransitivelyScopedNode(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runExplain("../../examples/scoped", "chat.RoomLog", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	if !strings.Contains(out, "*example.com/servoscoped/chat.RoomLog") {
		t.Errorf("explain could not find a transitively scoped node, got:\n%s", out)
	}
}

// TestRunWhyReachesThroughAnAccessor: an accessor is generated code
// rather than a resolved node, so a plain walk over Deps stops at the
// interface and reports every scoped node as unreachable from any root —
// which is exactly backwards.
func TestRunWhyReachesThroughAnAccessor(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runWhy("../../examples/scoped", "chat.RoomLog", false); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	for _, want := range []string{
		"root  *example.com/servoscoped/api.Server",
		"-> *example.com/servoscoped/chat.Room",
		"-> *example.com/servoscoped/chat.RoomLog",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("why output missing %q, got:\n%s", want, out)
		}
	}
}

func TestRunWhyScopedJSON(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runWhy("../../examples/scoped", "chat.Room", true); err != nil {
			t.Fatalf("runWhy: %v", err)
		}
	})
	if !strings.Contains(out, "chat.Room") || !strings.Contains(out, "api.Server") {
		t.Errorf("why --json output missing the path, got:\n%s", out)
	}
}

// TestRunGraphScopedFormats covers ToGraph's scope attribution through
// every renderer the CLI exposes.
func TestRunGraphScopedFormats(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []string
	}{
		{"text", []string{"══ example.com/servoscoped/chat.RoomKey ══", "linger: 30s   max: 10000", "── Scope level 1 ──"}},
		{"json", []string{`"scopes"`, `"key": "example.com/servoscoped/chat.RoomKey"`, `"borrows"`}},
		{"dot", []string{"subgraph cluster_scope0 {", `"example.com/servoscoped/chat.RoomKey"`}},
		{"mermaid", []string{"subgraph scope0[", ":::scopekey"}},
	} {
		t.Run(tc.format, func(t *testing.T) {
			out := captureStdout(t, func() {
				if err := runGraph("../../examples/scoped", tc.format); err != nil {
					t.Fatalf("runGraph: %v", err)
				}
			})
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("graph --format=%s missing %q, got:\n%s", tc.format, want, out)
				}
			}
		})
	}
}

// TestRunListSeesScopedProviders: the candidate index is built before
// resolution, so a scoped type's constructor is an ordinary candidate.
func TestRunListSeesScopedProviders(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runList("../../examples/scoped", false, false, false); err != nil {
			t.Fatalf("runList: %v", err)
		}
	})
	if !strings.Contains(out, "chat.NewRoom") {
		t.Errorf("list output missing the scoped type's constructor, got:\n%s", out)
	}
}
