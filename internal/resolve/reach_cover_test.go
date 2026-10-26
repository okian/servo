package resolve

import (
	"go/token"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
)

// lineContaining is the 1-based line of src's first line containing
// needle, so an assertion about a fixture position survives an edit above
// it.
func lineContaining(t *testing.T, src, needle string) int {
	t.Helper()
	for i, line := range strings.Split(src, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	t.Fatalf("no line contains %q", needle)
	return 0
}

// unreachableSummary renders Unreachable's answer one "Func — Reason" line
// per entry, in the order it came back.
func unreachableSummary(got []NearMiss) []string {
	out := make([]string, len(got))
	for i, m := range got {
		out[i] = m.Func + " — " + m.Reason
	}
	return out
}

const mainOnlySrc = "package main\n\nfunc main() {}\n"

// TestUnreachableExplainsEachPackageRule is `servo list --rejected`'s view
// of the candidates the index holds and this injector can never select:
// one per way a package can be out of the injector's reach, and one whose
// own package is fine but whose result type lives in a package that is
// not. They come back in source order — the main module first, then each
// dependency — so the list reads the same on every machine.
func TestUnreachableExplainsEachPackageRule(t *testing.T) {
	m := checkModule(t,
		// The first two stand for the standard library's vendored copies
		// and its own internal packages, both loaded as dependencies.
		pkgSrc{"vendor/example.org/lib", "package lib\n\ntype V struct{}\n\nfunc NewV() *V { return nil }\n"},
		pkgSrc{"internal/x", "package x\n\ntype T struct{}\n\nfunc NewT() *T { return nil }\n"},
		pkgSrc{"example.com/mod/svc/internal/db", "package db\n\ntype DB struct{}\n\nfunc NewDB() *DB { return nil }\n"},
		pkgSrc{"example.com/mod/svc/internal", "package internal\n\ntype Cfg struct{}\n\nfunc NewCfg() *Cfg { return nil }\n"},
		pkgSrc{"example.com/mod/svc", `package svc

import "example.com/mod/svc/internal/db"

// Open is declared somewhere the injector may import, and returns a type
// declared somewhere it may not.
func Open() *db.DB { return nil }

type Service struct{}

func NewService() *Service { return nil }
`},
		pkgSrc{"example.com/mod/cmd/app", mainOnlySrc})

	const internalRule = "which example.com/mod/cmd/app cannot import: it is internal to example.com/mod/svc — move one of the two so the injector sits inside that tree, or the type outside it"
	want := []string{
		"db.NewDB — it is declared in example.com/mod/svc/internal/db, " + internalRule,
		// A path that ends in internal is the internal directory itself.
		"internal.NewCfg — it is declared in example.com/mod/svc/internal, " + internalRule,
		"svc.Open — its result type *db.DB is declared in example.com/mod/svc/internal/db, " + internalRule,
		"x.NewT — it is declared in internal/x, which only the standard library may import",
		"lib.NewV — it is declared in vendor/example.org/lib, a vendored copy no other package may import",
	}
	got := Unreachable(m.input("example.com/mod/cmd/app"))
	for _, nm := range got {
		if nm.Kind != "unusable" {
			t.Errorf("%s has kind %q, want unusable", nm.Func, nm.Kind)
		}
	}
	if g := unreachableSummary(got); strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Errorf("Unreachable =\n%s\nwant\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}

	// Inside the svc tree the internal packages are importable, and only
	// the standard library's and the vendored ones stay out of reach.
	m = checkModule(t,
		pkgSrc{"vendor/example.org/lib", "package lib\n\ntype V struct{}\n\nfunc NewV() *V { return nil }\n"},
		pkgSrc{"internal/x", "package x\n\ntype T struct{}\n\nfunc NewT() *T { return nil }\n"},
		pkgSrc{"example.com/mod/svc/internal/db", "package db\n\ntype DB struct{}\n\nfunc NewDB() *DB { return nil }\n"},
		pkgSrc{"example.com/mod/svc/cmd/svcd", mainOnlySrc})
	got = Unreachable(m.input("example.com/mod/svc/cmd/svcd"))
	if g := unreachableSummary(got); len(g) != 2 || !strings.HasPrefix(g[0], "x.NewT — ") || !strings.HasPrefix(g[1], "lib.NewV — ") {
		t.Errorf("Unreachable from inside the tree = %q, want only x.NewT and lib.NewV", g)
	}

	// A Spec built by hand names no injector, and then nothing is judged.
	in := m.input("example.com/mod/svc/cmd/svcd")
	in.Spec = nil
	if got := Unreachable(in); len(got) != 0 {
		t.Errorf("Unreachable without an injector = %+v, want nothing", got)
	}
}

// reachCompSrc is a component package whose constructors are all fine Go,
// and whose result types each hide an unexported type somewhere the
// emitter would have to spell it: a type argument, and every composite a
// type argument can be built from. NewEmbedded's interface declares a
// method of its own beside the embedded one, so it is not identical to
// NewMethod's: identical type arguments share one instantiation of Box,
// and the embedded half would never be walked. NewNameable is built from
// the same composites around an exported type, so the walk has to come
// back empty for it — including the second time it meets Item.
const reachCompSrc = `package comp

type item struct{}

type Item struct{}

type Box[T any] struct{}

type Getter[T any] interface{ Get() T }

type evictor struct{}

func NewEvictor() evictor { return evictor{} }

func NewSlice() *Box[[]item]                                 { return nil }
func NewArray() *Box[[2]item]                                { return nil }
func NewChan() *Box[chan item]                               { return nil }
func NewMapKey() *Box[map[item]int]                          { return nil }
func NewMapValue() *Box[map[int]item]                        { return nil }
func NewParam() *Box[func(item)]                             { return nil }
func NewResult() *Box[func() item]                           { return nil }
func NewField() *Box[struct{ I item }]                       { return nil }
func NewMethod() *Box[interface{ Get() item }]               { return nil }
func NewEmbedded() *Box[interface{ Close(); Getter[item] }] { return nil }
func NewNested() *Box[*Box[item]]                            { return nil }

func NewNameable() *Box[map[Item]func(chan Item) struct{ A [2]Item; B interface{ Get() Item } }] { return nil }
`

// TestUnexportedTypeInsideAResultTypeIsFound: the App field holding a node
// is declared with the node's whole result type, so an unexported type
// anywhere inside it — not just at the top — is a field the generated file
// cannot declare. The walk follows the emitter through every composite,
// and the reason names the hidden type alongside the whole one.
func TestUnexportedTypeInsideAResultTypeIsFound(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/comp", reachCompSrc}, pkgSrc{"example.com/mod/cmd/app", mainOnlySrc})

	uses := func(written string) string {
		return "its result type " + written + " uses comp.item, which is unexported, so the App generated in example.com/mod/cmd/app cannot name it"
	}
	want := []string{
		// A value result is unexported as a whole: there is no pointer to
		// look through.
		"comp.NewEvictor — its result type comp.evictor is unexported, so the App generated in example.com/mod/cmd/app cannot name it — export the type, or return an exported interface it implements",
		"comp.NewSlice — " + uses("*comp.Box[[]comp.item]"),
		"comp.NewArray — " + uses("*comp.Box[[2]comp.item]"),
		"comp.NewChan — " + uses("*comp.Box[chan comp.item]"),
		"comp.NewMapKey — " + uses("*comp.Box[map[comp.item]int]"),
		"comp.NewMapValue — " + uses("*comp.Box[map[int]comp.item]"),
		"comp.NewParam — " + uses("*comp.Box[func(comp.item)]"),
		"comp.NewResult — " + uses("*comp.Box[func() comp.item]"),
		"comp.NewField — " + uses("*comp.Box[struct{I comp.item}]"),
		"comp.NewMethod — " + uses("*comp.Box[interface{Get() comp.item}]"),
		"comp.NewEmbedded — " + uses("*comp.Box[interface{Close(); comp.Getter[comp.item]}]"),
		"comp.NewNested — " + uses("*comp.Box[*comp.Box[comp.item]]"),
	}
	got := unreachableSummary(Unreachable(m.input("example.com/mod/cmd/app")))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Unreachable =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Generated into comp itself, every one of those types is nameable.
	if got := Unreachable(m.input("example.com/mod/comp")); len(got) != 0 {
		t.Errorf("Unreachable from inside comp = %q, want nothing", unreachableSummary(got))
	}
}

// reachConfigSrcs put a //servo:config type under svc/internal and a
// consumer of it in svc, so whether the graph can be generated depends on
// where the injector sits.
var reachConfigSrcs = []pkgSrc{
	{"example.com/mod/svc/internal/cfg", "package cfg\n\ntype Settings struct{ DSN string }\n"},
	{"example.com/mod/svc", `package svc

import "example.com/mod/svc/internal/cfg"

type Service struct{}

func NewService(s cfg.Settings) *Service { return &Service{} }
`},
	{"example.com/mod/cmd/app", mainOnlySrc},
	{"example.com/mod/svc/cmd/svcd", mainOnlySrc},
}

// reachConfigDecl is the declaration the config scan would produce for
// cfg.Settings.
func reachConfigDecl(m *modFixture) *graph.ConfigDecl {
	typ := m.typ("example.com/mod/svc/internal/cfg", "Settings")
	return &graph.ConfigDecl{
		Key: graph.NewKey(typ, ""), Type: typ, TypeName: "Settings",
		Prefix: "APP", Section: "app",
		PkgPath: "example.com/mod/svc/internal/cfg", PkgName: "cfg", Dir: "/mod/svc/internal/cfg",
		Pos:    token.Position{Filename: "/mod/svc/internal/cfg/x.go", Line: 3, Column: 6},
		Fields: []graph.ConfigField{{FieldName: "DSN", Name: "dsn", EnvName: "APP_DSN", FileKey: "dsn", Kind: graph.KindString}},
	}
}

// TestConfigTheInjectorCannotLoadIsRefused: New calls every used config's
// generated loader, which lives in the config type's own package, so a
// config internal to a tree the injector sits outside of would generate a
// file the compiler refuses. It is refused here instead, at the consumer
// that pulled it in and with the reason, and resolves normally for an
// injector inside the tree.
func TestConfigTheInjectorCannotLoadIsRefused(t *testing.T) {
	m := checkModule(t, reachConfigSrcs...)
	decl := reachConfigDecl(m)

	in := m.input("example.com/mod/cmd/app", m.ptr("example.com/mod/svc", "Service"))
	in.Configs = []*graph.ConfigDecl{decl}
	_, diags := Resolve(in)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1:\n%s", len(diags), diagText(diags))
	}
	msg := diags[0].Message
	for _, want := range []string{
		"servo: config type example.com/mod/svc/internal/cfg.Settings cannot be loaded by the injector in example.com/mod/cmd/app\n",
		"needed by *example.com/mod/svc.Service",
		"root ",
		"New calls the generated loader of every config its graph uses, and its loader, cfg.ServoConfig, is declared in example.com/mod/svc/internal/cfg, which example.com/mod/cmd/app cannot import: it is internal to example.com/mod/svc",
		"Declare the config type in a package the injector can import.\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	// The consumer's constructor is the line that pulled the config in.
	wantPos := token.Position{Filename: "/mod/svc/x.go", Line: lineContaining(t, reachConfigSrcs[1].src, "func NewService"), Column: 6}
	if diags[0].Pos.String() != wantPos.String() {
		t.Errorf("diagnostic at %v, want the consumer's constructor at %v", diags[0].Pos, wantPos)
	}

	in = m.input("example.com/mod/svc/cmd/svcd", m.ptr("example.com/mod/svc", "Service"))
	in.Configs = []*graph.ConfigDecl{decl}
	r := mustResolve(t, in)
	if len(r.Configs) != 1 || r.Configs[0].Config != decl {
		t.Fatalf("Configs = %v, want the one config, loaded from inside its tree", r.Configs)
	}
}

// internalConfigSrcs declare a //servo:config type in svc's internal cfg
// package, as decl spells it, with a consumer of it in svc.
func internalConfigSrcs(decl string) []pkgSrc {
	return []pkgSrc{
		{"example.com/mod/svc/internal/cfg", "package cfg\n\n//servo:config prefix=APP\n" + decl},
		reachConfigSrcs[1],
		{"example.com/mod/cmd/app", mainOnlySrc},
	}
}

// selfConfigSrcs alias the injector package's own struct from conf, a
// package that has to import the injector to do it.
var selfConfigSrcs = []pkgSrc{
	{"example.com/mod/app", "package app\n\ntype Settings struct {\n\tDSN string `config:\"dsn\"`\n}\n\ntype Server struct{}\n\nfunc NewServer(s Settings) *Server { return &Server{} }\n"},
	{"example.com/mod/conf", "package conf\n\nimport \"example.com/mod/app\"\n\n//servo:config prefix=APP\ntype Settings = app.Settings\n"},
	{"example.com/mod/cmd/app", mainOnlySrc},
}

// TestAliasedConfigIsJudgedByTheDirectivesPackage: the loader is generated
// beside the //servo:config directive, so the injector has to import the
// package holding the directive. For an alias, servo judged the package of
// the aliased type instead, which can be one the injector may import, and
// for an alias of a struct literal is no package at all, so both aliases
// below resolved into a file the compiler refuses. They are refused as a
// named type in the same package is, and an alias the injector can import
// still loads.
func TestAliasedConfigIsJudgedByTheDirectivesPackage(t *testing.T) {
	const internalWhy = "its loader, cfg.ServoConfig, is declared in example.com/mod/svc/internal/cfg, which example.com/mod/cmd/app cannot import: it is internal to example.com/mod/svc — move one of the two so the injector sits inside that tree, or the type outside it"
	for _, tc := range []struct {
		name     string
		srcs     []pkgSrc
		injector string
		root     [2]string // the root's package and type name
		why      string    // "" when the config loads
	}{{
		name:     "named struct under another tree's internal directory",
		srcs:     internalConfigSrcs("type Settings struct {\n\tDSN string `config:\"dsn\"`\n}\n"),
		injector: "example.com/mod/cmd/app", root: [2]string{"example.com/mod/svc", "Service"},
		why: internalWhy,
	}, {
		name:     "alias of a struct under another tree's internal directory",
		srcs:     internalConfigSrcs("type Settings = struct {\n\tDSN string `config:\"dsn\"`\n}\n"),
		injector: "example.com/mod/cmd/app", root: [2]string{"example.com/mod/svc", "Service"},
		why: internalWhy,
	}, {
		name: "alias of a struct the injector can import",
		srcs: []pkgSrc{
			{"example.com/mod/svc/cfg", "package cfg\n\n//servo:config prefix=APP\ntype Settings = struct {\n\tDSN string `config:\"dsn\"`\n}\n"},
			{"example.com/mod/svc", strings.ReplaceAll(reachConfigSrcs[1].src, "svc/internal/cfg", "svc/cfg")},
			{"example.com/mod/cmd/app", mainOnlySrc},
		},
		injector: "example.com/mod/cmd/app", root: [2]string{"example.com/mod/svc", "Service"},
	}, {
		name:     "alias in a package that imports the injector",
		srcs:     selfConfigSrcs,
		injector: "example.com/mod/app", root: [2]string{"example.com/mod/app", "Server"},
		why: "its loader, conf.ServoConfig, is declared in example.com/mod/conf, which imports example.com/mod/app, the injector's own package, so importing it back from there would be an import cycle — declare servo.Build in a package nothing imports, such as a cmd/ main",
	}, {
		// The fix that diagnostic names: from a cmd/ main, nothing closes
		// the cycle.
		name:     "the same alias with servo.Build in a cmd/ main",
		srcs:     selfConfigSrcs,
		injector: "example.com/mod/cmd/app", root: [2]string{"example.com/mod/app", "Server"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			m := checkModule(t, tc.srcs...)
			decls, err := graph.ScanConfigs(m.pkgs)
			if err != nil || len(decls) != 1 {
				t.Fatalf("ScanConfigs = %v, %v; want the one declaration", decls, err)
			}
			in := m.input(tc.injector, m.ptr(tc.root[0], tc.root[1]))
			in.Configs = decls
			if tc.why == "" {
				if r := mustResolve(t, in); len(r.Configs) != 1 || r.Configs[0].Config != decls[0] {
					t.Fatalf("Configs = %v, want the one config", r.Configs)
				}
				return
			}
			msg := onlyDiagnostic(t, in).Message
			for _, want := range []string{
				" cannot be loaded by the injector in " + tc.injector + "\n",
				"\n  New calls the generated loader of every config its graph uses, and " + tc.why + ".\n",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("diagnostic missing %q:\n%s", want, msg)
				}
			}
		})
	}
}
