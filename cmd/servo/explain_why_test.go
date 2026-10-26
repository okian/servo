package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
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
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Fatalf("got err=%v, want a 'ambiguous provider for' ambiguity diagnostic", err)
	}
}

// TestRunExplainASuppliedValue: every other node explain describes has a
// provider, and the three lines a reader looks at first — provider,
// position, binding — are all read off it. A servo.Value has none: the
// caller is the provider, and the node's Provider field is nil. So explain
// has to answer from the declaration instead, and the position it prints
// has to be the servo.Value line, which is the only place anyone can go to
// find out why the type turned up in Values in the first place.
func TestRunExplainASuppliedValue(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/explainvalue\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	mustWriteFile(t, dir, "conf/conf.go", `package conf

// Flags is parsed in main, so nothing in the graph can build it.
type Flags struct{ DSN string }

type Thing struct{ F Flags }

func NewThing(f Flags) *Thing { return &Thing{F: f} }
`)
	mustWriteFile(t, dir, "cmd/app/main.go", "package main\n\nfunc main() {}\n")
	mustWriteFile(t, dir, "cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/explainvalue/conf"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Value[conf.Flags](),
		servo.Root[*conf.Thing](),
	)
}
`)
	runGoModTidy(t, dir)

	out := captureStdout(t, func() {
		if err := runExplain(cfg(dir), "conf.Flags", false); err != nil {
			t.Fatalf("runExplain: %v", err)
		}
	})
	for _, want := range []string{
		"example.com/explainvalue/conf.Flags",
		"provider:     the caller, via NewWith",
		"binding:      supplied",
		"lifetime:     supplied — handed to NewWith once, held for the life of the process",
		// The position on the provider line: the servo.Value declaration.
		"spec.go",
		// And the dependent, which is the whole reason it has to be supplied.
		"conf.Thing",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output missing %q, got:\n%s", want, out)
		}
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
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider for") {
		t.Fatalf("got err=%v, want a 'ambiguous provider for' ambiguity diagnostic", err)
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

// writeQueryModule is a module shaped around the questions explain and
// why used to answer with a bare "no node matches": the root is an
// interface resolved by sole implementation, a parameter is an interface
// bound explicitly, a dependency is a generic instantiation, and three
// types are absent from the graph for three different reasons — a
// constructor no root reaches, an interface nothing asks for, and a
// constructor the scan rejected.
//
//	api.Handler (root, sole implementation) -> *api.Server
//	  -> store.Store (explicit bind) -> *postgres.DB -> *logger.Logger
//	  -> *repo.Repo[model.User]
//	  -> *logger.Logger
func writeQueryModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/q\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("logger/logger.go", "package logger\n\ntype Logger struct{}\n\nfunc New() *Logger { return &Logger{} }\n")
	write("model/model.go", "package model\n\ntype User struct{}\n")
	write("repo/repo.go", "package repo\n\nimport \"example.com/q/model\"\n\ntype Repo[T any] struct{}\n\nfunc NewUserRepo() *Repo[model.User] { return &Repo[model.User]{} }\n")
	write("store/store.go", "package store\n\ntype Store interface{ Get(key string) string }\n")
	write("postgres/postgres.go", "package postgres\n\nimport \"example.com/q/logger\"\n\ntype DB struct{}\n\nfunc New(l *logger.Logger) *DB { return &DB{} }\n\nfunc (d *DB) Get(key string) string { return key }\n")
	write("memory/memory.go", "package memory\n\ntype Mem struct{}\n\nfunc New() *Mem { return &Mem{} }\n\nfunc (m *Mem) Get(key string) string { return key }\n")
	write("notify/notify.go", "package notify\n\ntype Sender interface{ Send(msg string) error }\n")
	write("mail/mail.go", "package mail\n\ntype Mailer struct{}\n\nfunc New() *Mailer { return &Mailer{} }\n\nfunc (m *Mailer) Send(msg string) error { return nil }\n")
	write("widget/widget.go", "package widget\n\ntype Widget struct{}\n\nfunc newWidget() *Widget { return &Widget{} }\n\nvar _ = newWidget\n")
	write("api/api.go", `package api

import (
	"example.com/q/logger"
	"example.com/q/model"
	"example.com/q/repo"
	"example.com/q/store"
)

type Handler interface{ Handle() }

type Server struct{}

func New(s store.Store, r *repo.Repo[model.User], l *logger.Logger) *Server { return &Server{} }

func (s *Server) Handle() {}
`)
	// The spec imports every package, so they are all loaded whatever
	// the graph needs: memory, mail and widget are present and absent
	// from the graph, which is the point.
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/q/api"
	_ "example.com/q/mail"
	_ "example.com/q/memory"
	_ "example.com/q/notify"
	"example.com/q/postgres"
	"example.com/q/store"
	_ "example.com/q/widget"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[api.Handler](),
		servo.Bind[store.Store, *postgres.DB](),
	)
}
`)
	runGoModTidy(t, dir)
	return dir
}

// TestExplainAndWhyAnswerTheQueriesPeopleType covers each spelling that
// used to end in `no node matches`: Go's own pointer syntax, an
// interface — bound, or resolved by sole implementation — and a generic
// instantiation qualified by package name, as source code writes it.
func TestExplainAndWhyAnswerTheQueriesPeopleType(t *testing.T) {
	dir := writeQueryModule(t)
	explain := func(query string, jsonOut bool) string {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runExplain(cfg(dir), query, jsonOut) })
		if err != nil {
			t.Fatalf("explain %s: %v", query, err)
		}
		return out
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"*api.Server", []string{"*example.com/q/api.Server\n", "provider:     api.New"}},
		// The root is an interface; the node is what it resolved to.
		{"api.Handler", []string{
			"example.com/q/api.Handler resolves to *example.com/q/api.Server — the sole implementation in the main module",
			"binding:      sole implementation (of example.com/q/api.Handler)",
		}},
		// A Bind says where it was written down.
		{"store.Store", []string{
			"example.com/q/store.Store resolves to *example.com/q/postgres.DB — explicit bind, servo.Bind at " + filepath.Join(dir, "cmd", "app", "spec.go") + ":19:3",
			"binding:      explicit bind (example.com/q/store.Store, " + filepath.Join(dir, "cmd", "app", "spec.go") + ":19:3)",
		}},
		{"repo.Repo[model.User]", []string{"*example.com/q/repo.Repo[example.com/q/model.User]\n"}},
		{"*repo.Repo[model.User]", []string{"*example.com/q/repo.Repo[example.com/q/model.User]\n"}},
		{"repo.Repo", []string{"*example.com/q/repo.Repo[example.com/q/model.User]\n"}},
		{"Repo[ model.User ]", []string{"*example.com/q/repo.Repo[example.com/q/model.User]\n"}},
	}
	for _, c := range cases {
		out := explain(c.query, false)
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("explain %q missing %q:\n%s", c.query, want, out)
			}
		}
	}

	out := explain("store.Store", true)
	for _, want := range []string{`"via": {`, `"interface": "example.com/q/store.Store"`, `"binding": "explicit bind"`, `"binds": [`} {
		if !strings.Contains(out, want) {
			t.Errorf("explain --json store.Store missing %q:\n%s", want, out)
		}
	}

	// A star asks for a pointer, so it never lands on a value.
	if err := runExplain(cfg(dir), "*store.Store", false); err == nil {
		t.Error("explain *store.Store found a node; no pointer to an interface is in the graph")
	}
}

// TestExplainSaysWhyATypeIsNotInTheGraph: three absent types, three
// different reasons, each named instead of "no node matches".
func TestExplainSaysWhyATypeIsNotInTheGraph(t *testing.T) {
	dir := writeQueryModule(t)
	cases := []struct {
		query string
		run   func(load.Config, string, bool) error
		want  []string
	}{
		{"memory.Mem", runWhy, []string{
			`no node matches "memory.Mem"`,
			"memory.New (" + filepath.Join(dir, "memory", "memory.go") + ":5:6) provides *example.com/q/memory.Mem, but nothing in this graph depends on it, so no root reaches it",
			"servo.Root[*memory.Mem]()",
		}},
		{"notify.Sender", runExplain, []string{
			"example.com/q/notify.Sender is an interface, and nothing in this graph asks for it",
			"implemented by *example.com/q/mail.Mailer (mail.New, ",
		}},
		{"widget.Widget", runExplain, []string{
			"widget.newWidget (" + filepath.Join(dir, "widget", "widget.go") + ":5:6) returns it, but servo rejected it: unexported, outside injector package",
		}},
		{"nope.Nothing", runExplain, []string{`no node matches "nope.Nothing" — no type of that name`}},
	}
	for _, c := range cases {
		err := c.run(cfg(dir), c.query, false)
		if err == nil {
			t.Errorf("%q: got nil error, want an explanation", c.query)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: error missing %q:\n%v", c.query, want, err)
			}
		}
	}
}

// TestWhyShowsTheInterfaceAStepWasRequestedAs: api.Server takes
// store.Store, not *postgres.DB, and a path that skipped the interface
// answered "why" with a parameter list nobody wrote.
func TestWhyShowsTheInterfaceAStepWasRequestedAs(t *testing.T) {
	dir := writeQueryModule(t)
	out := captureStdout(t, func() {
		if err := runWhy(cfg(dir), "postgres.DB", false); err != nil {
			t.Fatalf("why: %v", err)
		}
	})
	for _, want := range []string{
		"root  *example.com/q/api.Server  (as example.com/q/api.Handler, sole implementation)\n",
		"  -> *example.com/q/postgres.DB  (as example.com/q/store.Store, explicit bind)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("why postgres.DB missing %q:\n%s", want, out)
		}
	}
}

// TestWhyAllListsEveryPath: logger.Logger is reached from api.Server
// directly and through postgres.DB, and the shortest path alone hid the
// second reason it is in the binary.
func TestWhyAllListsEveryPath(t *testing.T) {
	dir := writeQueryModule(t)
	out := captureStdout(t, func() {
		if err := runWhyPaths(cfg(dir), "logger.Logger", false, true); err != nil {
			t.Fatalf("why --all: %v", err)
		}
	})
	short := "root  *example.com/q/api.Server  (as example.com/q/api.Handler, sole implementation)\n  -> *example.com/q/logger.Logger\n"
	long := "  -> *example.com/q/postgres.DB  (as example.com/q/store.Store, explicit bind)\n  -> *example.com/q/logger.Logger\n"
	if !strings.Contains(out, short) || !strings.Contains(out, long) {
		t.Fatalf("why --all should list both paths:\n%s", out)
	}
	if strings.Index(out, short) > strings.Index(out, long) {
		t.Errorf("why --all should list the shortest path first:\n%s", out)
	}

	out = captureStdout(t, func() {
		if err := runWhyPaths(cfg(dir), "logger.Logger", true, true); err != nil {
			t.Fatalf("why --all --json: %v", err)
		}
	})
	var paths [][]string
	if err := json.Unmarshal([]byte(out), &paths); err != nil || len(paths) != 2 {
		t.Fatalf("why --all --json = %d paths (%v), want 2:\n%s", len(paths), err, out)
	}
	if len(paths[0]) != 2 || len(paths[1]) != 3 {
		t.Errorf("why --all --json paths = %v, want the 2-step path then the 3-step one", paths)
	}
}

// TestWhyAllStopsAtTheLimit: paths through shared nodes multiply, so the
// enumeration stops at the limit and says so.
func TestWhyAllStopsAtTheLimit(t *testing.T) {
	leaf := &resolve.Node{Key: graph.Key{Type: "leaf"}}
	a := &resolve.Node{Key: graph.Key{Type: "a"}, Deps: []*resolve.Node{leaf}}
	b := &resolve.Node{Key: graph.Key{Type: "b"}, Deps: []*resolve.Node{leaf}}
	root := &resolve.Node{Key: graph.Key{Type: "root"}, Deps: []*resolve.Node{a, b, leaf}}
	resolved := &resolve.Resolved{Roots: []*resolve.Node{root}}

	paths, more := allPathsTo(resolved, leaf, 2)
	if len(paths) != 2 || !more {
		t.Fatalf("allPathsTo with limit 2 = %d paths, more=%v; want 2 and more=true", len(paths), more)
	}
	paths, more = allPathsTo(resolved, leaf, 10)
	if len(paths) != 3 || more {
		t.Fatalf("allPathsTo with limit 10 = %d paths, more=%v; want all 3 and more=false", len(paths), more)
	}
	if len(paths[0].nodes) != 2 {
		t.Errorf("the shortest path (root -> leaf) should come first, got %d nodes", len(paths[0].nodes))
	}
}
