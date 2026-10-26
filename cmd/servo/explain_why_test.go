package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

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

// writeLookupModule is a module built around the answers a failed lookup
// gives. Two node types share a name (logger.Logger, audit.Logger), two
// requested interfaces share one (alpha.Store, beta.Store), and a generic
// is instantiated twice (repo.Repo). The rest is absent from the graph in
// every way absentError tells apart: two declared types of one name
// (gadget.Widget, gizmo.Widget), a type no function returns and an alias
// to it (meter.Gauge, shim.Gauge), a generic declaration (box.Box), an
// interface with more implementations than the message lists
// (plug.Plugin), one with none (hook.Hook), and a constructor that
// returns a value (plain.New).
func writeLookupModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/lk\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("logger/logger.go", "package logger\n\ntype Logger struct{}\n\nfunc New() *Logger { return &Logger{} }\n")
	write("audit/audit.go", "package audit\n\ntype Logger struct{}\n\nfunc New() *Logger { return &Logger{} }\n")
	write("alpha/alpha.go", "package alpha\n\ntype Store interface{ Get(key string) string }\n")
	write("beta/beta.go", "package beta\n\ntype Store interface{ Put(key string) }\n")
	write("pg/pg.go", "package pg\n\ntype DB struct{}\n\nfunc New() *DB { return &DB{} }\n\nfunc (d *DB) Get(key string) string { return key }\n")
	write("mem/mem.go", "package mem\n\ntype Mem struct{}\n\nfunc New() *Mem { return &Mem{} }\n\nfunc (m *Mem) Put(key string) {}\n")
	write("model/model.go", "package model\n\ntype User struct{}\n\ntype Order struct{}\n")
	write("repo/repo.go", "package repo\n\nimport \"example.com/lk/model\"\n\ntype Repo[T any] struct{}\n\nfunc NewUsers() *Repo[model.User] { return &Repo[model.User]{} }\n\nfunc NewOrders() *Repo[model.Order] { return &Repo[model.Order]{} }\n")
	write("api/api.go", `package api

import (
	"example.com/lk/alpha"
	"example.com/lk/audit"
	"example.com/lk/beta"
	"example.com/lk/logger"
	"example.com/lk/model"
	"example.com/lk/repo"
)

type Server struct{}

func New(a alpha.Store, b beta.Store, u *repo.Repo[model.User], o *repo.Repo[model.Order], l *logger.Logger, al *audit.Logger) *Server {
	return &Server{}
}
`)
	write("gadget/gadget.go", "package gadget\n\ntype Widget struct{}\n")
	write("gizmo/gizmo.go", "package gizmo\n\ntype Widget struct{}\n")
	write("meter/meter.go", "package meter\n\ntype Gauge struct{}\n")
	write("shim/shim.go", "package shim\n\nimport \"example.com/lk/meter\"\n\ntype Gauge = meter.Gauge\n")
	write("box/box.go", "package box\n\ntype Box[T any] struct{ v T }\n")
	write("hook/hook.go", "package hook\n\ntype Hook interface{ Fire() }\n")
	write("plain/plain.go", "package plain\n\ntype Plain struct{}\n\nfunc New() Plain { return Plain{} }\n")
	var plug strings.Builder
	plug.WriteString("package plug\n\ntype Plugin interface{ Name() string }\n")
	for i := range 11 {
		fmt.Fprintf(&plug, "\ntype P%[1]d struct{}\n\nfunc NewP%[1]d() *P%[1]d { return &P%[1]d{} }\n\nfunc (*P%[1]d) Name() string { return \"p%[1]d\" }\n", i)
	}
	write("plug/plug.go", plug.String())
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/lk/api"
	_ "example.com/lk/box"
	_ "example.com/lk/gadget"
	_ "example.com/lk/gizmo"
	_ "example.com/lk/hook"
	_ "example.com/lk/meter"
	_ "example.com/lk/plain"
	_ "example.com/lk/plug"
	_ "example.com/lk/shim"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
`)
	runGoModTidy(t, dir)
	return dir
}

// TestExplainNamesEveryAmbiguity: a query that matches more than one
// node or interface names all of them, at whichever stage of the lookup
// found them — the key match explain inherited, the package-name forms,
// or the interfaces the graph requested — rather than picking one or
// reporting "no node matches".
func TestExplainNamesEveryAmbiguity(t *testing.T) {
	dir := writeLookupModule(t)
	cases := []struct {
		query string
		want  []string
	}{
		{"Logger", []string{
			`servo: "Logger" matches multiple nodes, be more specific: `,
			"*example.com/lk/logger.Logger",
			"*example.com/lk/audit.Logger",
		}},
		{"repo.Repo", []string{
			`servo: "repo.Repo" matches multiple nodes, be more specific: `,
			"*example.com/lk/repo.Repo[example.com/lk/model.User]",
			"*example.com/lk/repo.Repo[example.com/lk/model.Order]",
		}},
		{"Store", []string{
			`servo: "Store" matches multiple interfaces, be more specific: example.com/lk/alpha.Store, example.com/lk/beta.Store`,
		}},
	}
	for _, c := range cases {
		err := runExplain(cfg(dir), c.query, false)
		if err == nil {
			t.Errorf("explain %q: got nil error, want an ambiguity", c.query)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("explain %q: error missing %q:\n%v", c.query, want, err)
			}
		}
	}

	// One more letter settles each of them.
	for _, query := range []string{"audit.Logger", "repo.Repo[model.Order]", "beta.Store"} {
		if err := runExplain(cfg(dir), query, false); err != nil {
			t.Errorf("explain %q: %v", query, err)
		}
	}
}

// TestExplainSaysWhatItKnowsAboutAnAbsentType: each way a type can be
// missing from the graph gets its own sentence, so the reader learns
// whether to add a root, a constructor, or nothing at all.
func TestExplainSaysWhatItKnowsAboutAnAbsentType(t *testing.T) {
	dir := writeLookupModule(t)
	cases := []struct {
		query string
		want  []string
		not   []string
	}{
		{"Widget", []string{`servo: no node matches "Widget" — and it names 2 types, none of them in this graph: `, "example.com/lk/gadget.Widget", "example.com/lk/gizmo.Widget"}, nil},
		// The alias stands for meter.Gauge, which is reported once.
		{"Gauge", []string{
			`servo: no node matches "Gauge" — example.com/lk/meter.Gauge is not in this graph` + "\n" +
				"  and no function in the module returns meter.Gauge or a pointer to it",
		}, []string{"names 2 types"}},
		// A generic is named by its declaration, type arguments dropped.
		{"box.Box[int]", []string{
			`servo: no node matches "box.Box[int]" — example.com/lk/box.Box[...] is not in this graph` + "\n" +
				"  and no function in the module returns box.Box[...] or a pointer to it",
		}, nil},
		{"hook.Hook", []string{
			"example.com/lk/hook.Hook is an interface, and nothing in this graph asks for it\n" +
				"  and no constructor in the main module returns a type implementing it",
		}, nil},
		// A star asks for a pointer the only constructor does not return.
		{"*plain.Plain", []string{
			"example.com/lk/plain.Plain is not in this graph\n  plain.New (" + filepath.Join(dir, "plain", "plain.go") + ":5:6) provides example.com/lk/plain.Plain, but nothing in this graph depends on it",
			"declare servo.Root[plain.Plain](), or take it as a constructor parameter (note: it returns the value example.com/lk/plain.Plain, not a pointer)",
		}, nil},
		// A dependency's type is found too, when the main module has none.
		{"strings.Builder", []string{`servo: no node matches "strings.Builder" — strings.Builder is not in this graph`}, nil},
		{"*", []string{`servo: no node matches "*" — no type of that name is in this graph`}, nil},
		// A query of nothing but spaces names nothing, and says no more.
		{"   ", []string{`servo: no node matches "   "`}, []string{"—"}},
	}
	for _, c := range cases {
		err := runExplain(cfg(dir), c.query, false)
		if err == nil {
			t.Errorf("explain %q: got nil error, want an explanation", c.query)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("explain %q: error missing %q:\n%v", c.query, want, err)
			}
		}
		for _, not := range c.not {
			if strings.Contains(err.Error(), not) {
				t.Errorf("explain %q: error should not contain %q:\n%v", c.query, not, err)
			}
		}
	}

	// An interface with more implementations than one message holds lists
	// ten and points at `servo list` for the rest.
	err := runExplain(cfg(dir), "plug.Plugin", false)
	if err == nil {
		t.Fatal("explain plug.Plugin: got nil error")
	}
	msg := err.Error()
	if got := strings.Count(msg, "  implemented by *example.com/lk/plug.P"); got != 10 {
		t.Errorf("explain plug.Plugin lists %d implementations, want 10:\n%s", got, msg)
	}
	if !strings.HasSuffix(msg, "  … and more; `servo list` has every candidate") {
		t.Errorf("explain plug.Plugin should end by pointing at servo list:\n%s", msg)
	}
}

// TestExplainNamesWhereAnIncludedBindCameFrom builds on the marker-set
// module: the servo.Bind that selects *postgres.DB is spliced in from
// wiring.Shared, so neither the spec file nor anything near it shows it,
// and explain has to say so. The node is also requested as a second
// interface, so its hops are listed in interface order, and the graph
// holds a supplied value and a config, which a pointer query has to look
// past without matching.
func TestExplainNamesWhereAnIncludedBindCameFrom(t *testing.T) {
	dir := valueAndIncludeModule(t)
	mustWriteFile(t, dir, "cache/cache.go", "package cache\n\ntype Cache interface{ Get(key string) string }\n")
	mustWriteFile(t, dir, "settings/settings.go", "package settings\n\n//servo:config prefix=APP\ntype Settings struct {\n\tRegion string `config:\"region,default=eu\"`\n}\n")
	mustWriteFile(t, dir, "api/api.go", `package api

import (
	"example.com/mk/cache"
	"example.com/mk/settings"
	"example.com/mk/store"
)

type Server struct{}

func New(s store.Store, c cache.Cache, st settings.Settings) *Server { return &Server{} }
`)
	runGoModTidy(t, dir)
	api := cfg(filepath.Join(dir, "cmd", "api"))
	bindPos := filepath.Join(dir, "wiring", "wiring.go") + ":15:3"
	explain := func(query string) string {
		t.Helper()
		var err error
		out := captureStdout(t, func() { err = runExplain(api, query, false) })
		if err != nil {
			t.Fatalf("explain %s: %v", query, err)
		}
		return out
	}

	out := explain("postgres.DB")
	if want := "binding:      explicit bind (example.com/mk/store.Store, " + bindPos + ", via servo.Include)\n"; !strings.Contains(out, want) {
		t.Errorf("explain postgres.DB missing %q:\n%s", want, out)
	}

	out = explain("store.Store")
	if want := "example.com/mk/store.Store resolves to *example.com/mk/postgres.DB — explicit bind, servo.Bind at " + bindPos + ", in a marker set pulled in with servo.Include\n"; !strings.Contains(out, want) {
		t.Errorf("explain store.Store missing %q:\n%s", want, out)
	}

	// cache.Cache reaches the same node by sole implementation.
	out = explain("cache.Cache")
	if want := "example.com/mk/cache.Cache resolves to *example.com/mk/postgres.DB — the sole implementation in the main module\n"; !strings.Contains(out, want) {
		t.Errorf("explain cache.Cache missing %q:\n%s", want, out)
	}

	// Go's pointer syntax is not a key suffix, so this goes through every
	// node's spellings — conf.Flags, a value, and settings.Settings, a
	// config, among them — and lands on the one pointer that matches.
	out = explain("*api.Server")
	if !strings.HasPrefix(out, "*example.com/mk/api.Server\n") {
		t.Errorf("explain *api.Server should describe *api.Server, got:\n%s", out)
	}
	if err := runExplain(api, "*conf.Flags", false); err == nil || !strings.Contains(err.Error(), "example.com/mk/conf.Flags is not in this graph") {
		t.Errorf("explain *conf.Flags: a supplied value is not a pointer, got %v", err)
	}
	if err := runExplain(api, "*settings.Settings", false); err == nil || !strings.Contains(err.Error(), "example.com/mk/settings.Settings is not in this graph") {
		t.Errorf("explain *settings.Settings: a config is not a pointer, got %v", err)
	}
}

// TestExplainAScopeAccessorInterface: a consumer holds the accessor
// interface, never the scoped type, so that is what people ask about.
// The scope's key extractor takes a dependency of its own here, which is
// a request like any constructor parameter.
func TestExplainAScopeAccessorInterface(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }
	write("go.mod", "module example.com/sx\n\ngo 1.26.0\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	write("logger/logger.go", "package logger\n\ntype Logger struct{}\n\nfunc New() *Logger { return &Logger{} }\n")
	write("room/room.go", `package room

import (
	"context"

	"example.com/sx/logger"
	"github.com/okian/servo/v3/servo"
)

type Key string

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
	Stats() servo.ScopeStats
}

type Room struct{ key Key }

func New(key Key) *Room { return &Room{key: key} }

func (*Room) ScopeKey(ctx context.Context, l *logger.Logger) (Key, error) { return "k", nil }
`)
	write("api/api.go", "package api\n\nimport \"example.com/sx/room\"\n\ntype Server struct{ rooms room.Rooms }\n\nfunc New(r room.Rooms) *Server { return &Server{rooms: r} }\n")
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"example.com/sx/api"
	"example.com/sx/room"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Scoped[*room.Room, room.Rooms](),
	)
}
`)
	runGoModTidy(t, dir)

	out := captureStdout(t, func() {
		if err := runExplain(cfg(dir), "room.Rooms", false); err != nil {
			t.Fatalf("explain room.Rooms: %v", err)
		}
	})
	want := "example.com/sx/room.Rooms resolves to *example.com/sx/room.Room — the scope accessor servo generates for the servo.Scoped at " +
		filepath.Join(dir, "cmd", "app", "spec.go") + ":14:3; each acquire hands out one\n"
	if !strings.Contains(out, want) {
		t.Errorf("explain room.Rooms missing %q:\n%s", want, out)
	}
	if !strings.Contains(out, "depended on:  (acquired via example.com/sx/room.Rooms)") {
		t.Errorf("explain room.Rooms should name the accessor as the consumer:\n%s", out)
	}
}

// TestExplainAndWhyThroughATraceWrapper: the trace module's graph has a
// wrapper node with no provider of its own, and an Override that puts
// memory.Mem only in NewTestApp's graph.
func TestExplainAndWhyThroughATraceWrapper(t *testing.T) {
	dir := writeTraceModule(t)
	spec := filepath.Join(dir, "cmd", "app", "spec.go")

	// A pointer query reads every node's type, the wrapper's included.
	out := captureStdout(t, func() {
		if err := runExplain(cfg(dir), "*postgres.DB", false); err != nil {
			t.Fatalf("explain *postgres.DB: %v", err)
		}
	})
	if !strings.HasPrefix(out, "*example.com/tracefix/postgres.DB\n") {
		t.Errorf("explain *postgres.DB should describe *postgres.DB, got:\n%s", out)
	}

	// New's graph holds postgres.DB; the Override is NewTestApp's alone,
	// which is the one thing about memory.Mem worth saying.
	err := runExplain(cfg(dir), "memory.Mem", false)
	if err == nil {
		t.Fatal("explain memory.Mem: got nil error; memory.Mem is only in the test graph")
	}
	if want := "  servo.Override at " + spec + ":17:3 puts it in NewTestApp's graph in place of example.com/tracefix/store.Store; explain and why answer for New's"; !strings.Contains(err.Error(), want) {
		t.Errorf("explain memory.Mem missing %q:\n%v", want, err)
	}

	// The tracer provider is reached through the wrapper, which asked for
	// it as no constructor parameter.
	out = captureStdout(t, func() {
		if err := runWhy(cfg(dir), "telemetry.Provider", false); err != nil {
			t.Fatalf("why telemetry.Provider: %v", err)
		}
	})
	if !strings.Contains(out, "*example.com/tracefix/telemetry.Provider\n") || !strings.Contains(out, "root  *example.com/tracefix/api.Server") {
		t.Errorf("why telemetry.Provider should trace from api.Server to the provider:\n%s", out)
	}
}

// TestWhyAllSaysWhenThePathsRunPastTheLimit: a lattice five wide and
// three deep reaches its leaf by 125 paths, more than `why --all` prints,
// and the output has to say that the list stops short.
func TestWhyAllSaysWhenThePathsRunPastTheLimit(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "go.mod", "module example.com/lat\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRoot(t)+"\n")
	var src strings.Builder
	src.WriteString("package lat\n\ntype Leaf struct{}\n\nfunc NewLeaf() *Leaf { return &Leaf{} }\n")
	params := func(layer string) string {
		ps := make([]string, 5)
		for i := range ps {
			ps[i] = fmt.Sprintf("%s%d *%s%d", strings.ToLower(layer), i, layer, i)
		}
		return strings.Join(ps, ", ")
	}
	for i := range 5 {
		fmt.Fprintf(&src, "\ntype C%[1]d struct{}\n\nfunc NewC%[1]d(l *Leaf) *C%[1]d { return &C%[1]d{} }\n", i)
		fmt.Fprintf(&src, "\ntype B%[1]d struct{}\n\nfunc NewB%[1]d(%[2]s) *B%[1]d { return &B%[1]d{} }\n", i, params("C"))
		fmt.Fprintf(&src, "\ntype A%[1]d struct{}\n\nfunc NewA%[1]d(%[2]s) *A%[1]d { return &A%[1]d{} }\n", i, params("B"))
	}
	fmt.Fprintf(&src, "\ntype Top struct{}\n\nfunc NewTop(%s) *Top { return &Top{} }\n", params("A"))
	mustWriteFile(t, dir, "lat/lat.go", src.String())
	mustWriteFile(t, dir, "cmd/app/spec.go", "//go:build servoinject\n\npackage main\n\nimport (\n\t\"example.com/lat/lat\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nfunc wire() {\n\tservo.Build(servo.Root[*lat.Top]())\n}\n")
	runGoModTidy(t, dir)

	out := captureStdout(t, func() {
		if err := runWhyPaths(cfg(dir), "lat.Leaf", false, true); err != nil {
			t.Fatalf("why --all: %v", err)
		}
	})
	if got := strings.Count(out, "root  *example.com/lat/lat.Top\n"); got != whyPathLimit {
		t.Errorf("why --all printed %d paths, want the limit, %d", got, whyPathLimit)
	}
	if want := "\n… and more: only the first 100 paths are shown — `servo explain` on a node along them narrows the question\n"; !strings.HasSuffix(out, want) {
		t.Errorf("why --all should end with %q, got the tail:\n%s", want, out[max(0, len(out)-300):])
	}
}

// TestWhyAllSkipsARootThatCannotReachTheTarget: worker.Consumer is a
// root, but postgres.DB is not below it, so it contributes no path.
func TestWhyAllSkipsARootThatCannotReachTheTarget(t *testing.T) {
	dir := writeAppModule(t, "example.com/whyallskip", true, "")
	out := captureStdout(t, func() {
		if err := runWhyPaths(cfg(dir), "postgres.DB", false, true); err != nil {
			t.Fatalf("why --all: %v", err)
		}
	})
	if strings.Count(out, "root  ") != 1 || !strings.Contains(out, "root  *example.com/whyallskip/api.Server") || strings.Contains(out, "worker.Consumer") {
		t.Errorf("why --all postgres.DB should list the one path from api.Server:\n%s", out)
	}
}

// TestWhyAllFollowsTheHTTPEntryPoints: after the roots, every
// declaration of the HTTP surface that reaches the target is a reason
// too — one that cannot is skipped — and the limit still applies there.
func TestWhyAllFollowsTheHTTPEntryPoints(t *testing.T) {
	leaf := &resolve.Node{Key: graph.Key{Type: "leaf"}}
	mid := &resolve.Node{Key: graph.Key{Type: "mid"}, Deps: []*resolve.Node{leaf}}
	root := &resolve.Node{Key: graph.Key{Type: "root"}, Deps: []*resolve.Node{leaf}}
	unrelated := &resolve.Node{Key: graph.Key{Type: "config"}}
	resolved := &resolve.Resolved{
		Roots: []*resolve.Node{root},
		HTTP:  &resolve.HTTPPlan{Config: unrelated, Uses: []*resolve.HTTPUse{{Node: mid}}},
	}

	paths, more := allPathsTo(resolved, leaf, 10)
	if len(paths) != 2 || more {
		t.Fatalf("allPathsTo = %d paths, more=%v; want the root's and the middleware's, and no more", len(paths), more)
	}
	if paths[1].entry == nil || paths[1].entry.label != "servo.Use[mid], server" || len(paths[1].nodes) != 2 {
		t.Errorf("the second path should start at the servo.Use declaration and run mid -> leaf, got %+v", paths[1])
	}

	paths, more = allPathsTo(resolved, leaf, 1)
	if len(paths) != 1 || !more || paths[0].entry != nil {
		t.Errorf("allPathsTo with limit 1 = %d paths, more=%v; want the root's path alone and more=true", len(paths), more)
	}
}

// The lookup helpers tolerate a pipeline that loaded nothing, a rejected
// entry from a package the run did not load, and a package without type
// information, by finding nothing rather than panicking.
func TestExplainLookupHelpersTolerateMissingPackages(t *testing.T) {
	if err := absentError(nil, "api.Server"); err == nil || err.Error() != `servo: no node matches "api.Server"` {
		t.Errorf("absentError(nil) = %v, want the bare no-match error", err)
	}
	p := &pipeline{loaded: &load.Loaded{
		ByPath: map[string]*packages.Package{},
		All:    []*packages.Package{{PkgPath: "example.com/broken"}},
	}}
	if fn := rejectedFunc(p, graph.Rejected{Pkg: "example.com/gone", Name: "gone.New"}); fn != nil {
		t.Errorf("rejectedFunc for an unloaded package = %v, want nil", fn)
	}
	if found := typesNamed(p, "broken.Thing"); len(found) != 0 {
		t.Errorf("typesNamed over a package with no types = %v, want none", found)
	}
}
