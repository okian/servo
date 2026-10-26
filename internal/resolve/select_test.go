package resolve

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// pkgSrc is one package of a multi-package fixture. A path outside
// example.com/mod is a dependency module rather than the main module.
type pkgSrc struct{ path, src string }

// modFixture is a fixture module checked package by package, in order, so
// later sources can import earlier ones (and "context").
type modFixture struct {
	fset  *token.FileSet
	pkgs  []*packages.Package
	types map[string]*types.Package
}

const fixtureModule = "example.com/mod"

func checkModule(t *testing.T, srcs ...pkgSrc) *modFixture {
	t.Helper()
	m := &modFixture{fset: token.NewFileSet(), types: map[string]*types.Package{}}
	imp := importerFor(loadPkg(t, "context"))
	for _, s := range srcs {
		modPath, dir := fixtureModule, "/mod"
		if !strings.HasPrefix(s.path, fixtureModule) {
			modPath, dir = s.path, "/deps/"+s.path
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(s.path, modPath), "/")
		filename := dir + "/" + rel + "/x.go"
		f, err := parser.ParseFile(m.fset, filename, s.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", s.path, err)
		}
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		pkg, err := (&types.Config{Importer: imp}).Check(s.path, m.fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("typecheck %s: %v", s.path, err)
		}
		imp.byPath[s.path] = pkg
		m.types[s.path] = pkg
		m.pkgs = append(m.pkgs, &packages.Package{
			Name: pkg.Name(), PkgPath: s.path, Types: pkg, TypesInfo: info, Fset: m.fset,
			Syntax: []*ast.File{f}, Module: &packages.Module{Path: modPath, Dir: dir, Main: modPath == fixtureModule},
		})
	}
	return m
}

func (m *modFixture) pkg(path string) *packages.Package {
	for _, p := range m.pkgs {
		if p.PkgPath == path {
			return p
		}
	}
	panic("no fixture package " + path)
}

func (m *modFixture) typ(path, name string) types.Type {
	return m.types[path].Scope().Lookup(name).Type()
}

func (m *modFixture) ptr(path, name string) types.Type { return types.NewPointer(m.typ(path, name)) }

// input resolves roots for the injector at injector, with the candidate
// scan that injector's pipeline would run and the main module as scope.
func (m *modFixture) input(injector string, roots ...types.Type) Input {
	scope := map[string]bool{}
	for _, p := range m.pkgs {
		if p.Module.Main {
			scope[p.PkgPath] = true
		}
	}
	candidates, _ := graph.ScanCandidates(m.pkgs, injector)
	spec := &load.Spec{InjectorPkg: m.pkg(injector)}
	for i, r := range roots {
		spec.Roots = append(spec.Roots, load.RootDecl{Key: graph.NewKey(r, ""), Type: r, Pos: token.Position{Filename: "/mod/spec.go", Line: 10 + i}})
	}
	return Input{Spec: spec, Candidates: candidates, Caps: graph.EmptyCapabilities(), Scope: scope, Fset: m.fset, Pkgs: m.pkgs}
}

func mustResolve(t *testing.T, in Input) *Resolved {
	t.Helper()
	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	return resolved
}

func mustFail(t *testing.T, in Input) string {
	t.Helper()
	_, diags := Resolve(in)
	if len(diags) == 0 {
		t.Fatal("expected resolution to fail")
	}
	return diagText(diags)
}

func providerOf(t *testing.T, r *Resolved, typ types.Type) string {
	t.Helper()
	n := r.ByKey[graph.NewKey(typ, "")]
	if n == nil || n.Provider == nil {
		t.Fatalf("no provider-built node for %s", graph.TypeString(typ))
	}
	return n.Provider.Name
}

const libSrc = `package lib

type Logger struct{}

func New() *Logger     { return &Logger{} }
func Default() *Logger { return &Logger{} }

type Handler interface{ Serve() }

func HandlerA() Handler { return nil }
func HandlerB() Handler { return nil }

type Clock struct{}

func NewClock() *Clock { return &Clock{} }
`

// TestExactTypePrefersTheMainModule: a module's own constructor for a type
// a dependency also constructs used to make a three-way ambiguity whose
// advice — remove or rename all but one — nobody could take about the
// standard library. The module's own constructors decide.
func TestExactTypePrefersTheMainModule(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", libSrc}, pkgSrc{"example.com/mod/app", `package app

import "example.org/lib"

func NewLogger() *lib.Logger { return &lib.Logger{} }

type Server struct{}

func NewServer(l *lib.Logger, c *lib.Clock) *Server { return &Server{} }
`})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	if got := providerOf(t, r, m.ptr("example.org/lib", "Logger")); got != "app.NewLogger" {
		t.Errorf("*lib.Logger provided by %s, want the module's own app.NewLogger", got)
	}
	// A lone outside constructor is still selected when the module has
	// none, exactly as before.
	if got := providerOf(t, r, m.ptr("example.org/lib", "Clock")); got != "lib.NewClock" {
		t.Errorf("*lib.Clock provided by %s, want the sole candidate lib.NewClock", got)
	}
}

// TestForeignAmbiguityAdvisesWritingAProvider: several outside
// constructors and none of the module's own is still an ambiguity, but its
// advice is what the reader can do — write one, or supply a Value — with
// types spelled the way the spec file would spell them.
func TestForeignAmbiguityAdvisesWritingAProvider(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", libSrc}, pkgSrc{"example.com/mod/app", `package app

import "example.org/lib"

type Server struct{}

func NewServer(l *lib.Logger) *Server { return &Server{} }
`})
	msg := mustFail(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	for _, want := range []string{
		"servo: ambiguous provider for *example.org/lib.Logger",
		"2 functions outside this module produce *lib.Logger",
		"lib.New ", "lib.Default ",
		"Write its provider in this module",
		"func NewLogger(...) *lib.Logger",
		"servo.Value[*lib.Logger]()",
		"\"example.org/lib\"",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "remove or rename") {
		t.Errorf("diagnostic still tells the reader to rename functions they do not own:\n%s", msg)
	}
}

// TestForeignExactWaitsForStructural: several outside functions returning
// an interface exactly no longer pre-empt the module's own implementation
// of it.
func TestForeignExactWaitsForStructural(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", libSrc}, pkgSrc{"example.com/mod/app", `package app

import "example.org/lib"

type Mine struct{}

func (*Mine) Serve()      {}
func NewMine() *Mine      { return &Mine{} }

type Server struct{}

func NewServer(h lib.Handler) *Server { return &Server{} }
`})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	if got := r.ByKey[graph.NewKey(m.ptr("example.com/mod/app", "Mine"), "")]; got == nil || got.Binding != "sole implementation" {
		t.Fatalf("lib.Handler did not auto-bind to the module's *app.Mine: %+v", got)
	}
}

// TestUnexportedResultTypeIsUnusable: `func NewPool() *pool` is fine Go,
// but the App field holding it cannot be declared outside package comp.
// It used to be selected, and `servo generate` then called its own output's
// compile error a bug in servo.
func TestUnexportedResultTypeIsUnusable(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.com/mod/comp", `package comp

type pool struct{}

func NewPool() *pool { return &pool{} }

type Service struct{}

func NewService(p *pool) *Service { return &Service{} }
`},
		pkgSrc{"example.com/mod/cmd/app", "package main\n\nfunc main() {}\n"})
	msg := mustFail(t, m.input("example.com/mod/cmd/app", m.ptr("example.com/mod/comp", "Service")))
	for _, want := range []string{
		"no usable provider for *example.com/mod/comp.pool",
		"unusable: comp.NewPool (/mod/comp/x.go:5:6) — its result type *comp.pool is unexported, so the App generated in example.com/mod/cmd/app cannot name it",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	// Generated into comp itself, the field is writable and the
	// constructor is a candidate like any other.
	mustResolve(t, m.input("example.com/mod/comp", m.ptr("example.com/mod/comp", "Service")))
}

// TestInternalProviderIsUnusable applies Go's internal rule: only the tree
// rooted at svc may import svc/internal/db.
func TestInternalProviderIsUnusable(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.com/mod/svc/internal/db", "package db\n\ntype DB struct{}\n\nfunc NewDB() *DB { return &DB{} }\n"},
		pkgSrc{"example.com/mod/svc", `package svc

import "example.com/mod/svc/internal/db"

type Service struct{}

func NewService(d *db.DB) *Service { return &Service{} }
`},
		pkgSrc{"example.com/mod/cmd/app", "package main\n\nfunc main() {}\n"},
		pkgSrc{"example.com/mod/svc/cmd/svcd", "package main\n\nfunc main() {}\n"})
	msg := mustFail(t, m.input("example.com/mod/cmd/app", m.ptr("example.com/mod/svc", "Service")))
	if !strings.Contains(msg, "unusable: db.NewDB") || !strings.Contains(msg, "which example.com/mod/cmd/app cannot import: it is internal to example.com/mod/svc") {
		t.Errorf("diagnostic does not explain the internal rule:\n%s", msg)
	}
	// An injector inside the tree may use it.
	mustResolve(t, m.input("example.com/mod/svc/cmd/svcd", m.ptr("example.com/mod/svc", "Service")))
}

const cycleSrc = `package app

type Store interface{ Get() string }

type Server struct{}

func NewServer(s Store) *Server { return &Server{} }
`

const pluginSrc = `package plugin

import "example.com/mod/app"

type PG struct{}

func (*PG) Get() string { return "" }

func NewPG() *PG { return &PG{} }

var _ app.Store = (*PG)(nil)
`

// TestImportCycleProviderIsUnusable: an adapter importing the core package
// it serves cannot be called from a spec in that core package. Dropped
// from structural search, it leaves no provider — with the reason — and no
// longer makes an ambiguity of an implementation that is usable.
func TestImportCycleProviderIsUnusable(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", cycleSrc}, pkgSrc{"example.com/mod/plugin", pluginSrc})
	msg := mustFail(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	for _, want := range []string{
		"no provider for example.com/mod/app.Store",
		"unusable: plugin.NewPG",
		"which imports example.com/mod/app, the injector's own package, so importing it back from there would be an import cycle",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}

	m = checkModule(t, pkgSrc{"example.com/mod/app", cycleSrc}, pkgSrc{"example.com/mod/plugin", pluginSrc}, pkgSrc{"example.com/mod/memory", `package memory

type Mem struct{}

func (*Mem) Get() string { return "" }

func New() *Mem { return &Mem{} }
`})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	if got := providerOf(t, r, m.ptr("example.com/mod/memory", "Mem")); got != "memory.New" {
		t.Errorf("app.Store bound to %s, want memory.New — the unusable plugin is not a rival", got)
	}

	// A Bind naming the unusable type is refused with the same reason.
	m = checkModule(t, pkgSrc{"example.com/mod/app", cycleSrc}, pkgSrc{"example.com/mod/plugin", pluginSrc})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.Binds = []load.BindDecl{{
		Iface: graph.NewKey(m.typ("example.com/mod/app", "Store"), ""), IfaceType: m.typ("example.com/mod/app", "Store"),
		Concrete: graph.NewKey(m.ptr("example.com/mod/plugin", "PG"), ""), ConcreteType: m.ptr("example.com/mod/plugin", "PG"),
	}}
	if msg := mustFail(t, in); !strings.Contains(msg, "no usable provider for *example.com/mod/plugin.PG") || !strings.Contains(msg, "import cycle") {
		t.Errorf("a Bind to an unusable provider was not refused with its reason:\n%s", msg)
	}
}

// TestNearMissesNameTheFunctionThatAlmostProvided: "no provider" used to
// stop there, with the constructor the reader wrote sitting in plain view.
func TestNearMissesNameTheFunctionThatAlmostProvided(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", `package app

type Option func(*Client)
type Client struct{}

func NewClient(opts ...Option) *Client { return &Client{} }

type Config struct{}

func NewConfig() Config { return Config{} }

type Hosts []string

type Store interface{ Get() string }
type Mem struct{}

func (*Mem) Get() string { return "" }
func NewMem() Mem        { return Mem{} }

type Pool struct{}

var NewPool = func() *Pool { return &Pool{} }

type Cache[T any] struct{}

func NewCache[T any]() *Cache[T] { return nil }

type Server struct{}

func NewServer(c *Client, cfg *Config, s Store, p *Pool, k *Cache[string], h []string) *Server {
	return &Server{}
}
`})
	msg := mustFail(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	for _, want := range []string{
		"rejected: app.NewClient (/mod/app/x.go:6:6) — variadic parameter — unsupported",
		"near miss: app.NewConfig (/mod/app/x.go:10:6) — returns app.Config, not *app.Config — a type and a pointer to it are two different nodes",
		"near miss: app.NewMem (/mod/app/x.go:18:6) — returns app.Mem, which does not implement app.Store: Get has a pointer receiver, so only *app.Mem does",
		"rejected: app.NewPool (/mod/app/x.go:22:5) — a package-level variable of function type, not a function",
		"rejected: app.NewCache (/mod/app/x.go:26:6) — generic function — unsupported",
		"note: []string is never provided: it is a slice",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestNearMissesAPI is the engine `servo explain` and `servo why` share
// with the diagnostics, asked directly.
func TestNearMissesAPI(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", "package app\n\ntype Client struct{}\n\nfunc NewClient(n ...int) *Client { return nil }\n"})
	in := m.input("example.com/mod/app")
	got := NearMisses(in, m.ptr("example.com/mod/app", "Client"))
	if len(got) != 1 || got[0].Kind != "rejected" || got[0].Func != "app.NewClient" || got[0].Reason != "variadic parameter — unsupported" {
		t.Fatalf("NearMisses = %+v, want the one rejected variadic constructor", got)
	}
	if s := got[0].String(); s != "rejected: app.NewClient (/mod/app/x.go:5:6) — variadic parameter — unsupported" {
		t.Errorf("String() = %q", s)
	}
}

const ctxSrc = `package app

import "context"

type DB struct{}

func NewDB(ctx context.Context) (*DB, error) { return &DB{}, nil }

type Server struct{}

func NewServer(d *DB) *Server { return &Server{} }
`

// TestContextParameterIsTheConstructionContext: a constructor's
// context.Context is the ctx New was called with — not a search, which
// found ten standard-library functions and called it an ambiguity.
func TestContextParameterIsTheConstructionContext(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", ctxSrc})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server")))
	db := r.ByKey[graph.NewKey(m.ptr("example.com/mod/app", "DB"), "")]
	if db == nil || len(db.Deps) != 1 || db.Deps[0].Kind != NodeContext || db.Deps[0].Provider != nil {
		t.Fatalf("NewDB's ctx did not resolve to the construction context: %+v", db)
	}
	if db.Level != 1 {
		t.Errorf("DB level = %d, want 1: the construction context sits at 0", db.Level)
	}
	if len(r.Supplied) != 0 {
		t.Errorf("the construction context made a Values field: %v", r.Supplied)
	}

	// A declared servo.Value still wins, as it always has.
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	ctxType := loadPkg(t, "context").Types.Scope().Lookup("Context").Type()
	in.Spec.Values = []load.ValueDecl{{Key: graph.NewKey(ctxType, ""), Type: ctxType, Pos: token.Position{Filename: "/mod/spec.go", Line: 20}}}
	r = mustResolve(t, in)
	db = r.ByKey[graph.NewKey(m.ptr("example.com/mod/app", "DB"), "")]
	if db.Deps[0].Kind != NodeSupplied {
		t.Errorf("with servo.Value[context.Context] declared, NewDB's ctx is %v, want the supplied value", db.Deps[0].Kind)
	}
}

// TestScopedConstructorCannotTakeTheConstructionContext: a scoped instance
// is built inside Acquire, after New returned, so there is no construction
// context to hand it.
func TestScopedConstructorCannotTakeTheConstructionContext(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", `package app

import "context"

type RoomKey string

type Room struct{}

func NewRoom(ctx context.Context, k RoomKey) *Room { return &Room{} }

func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms) *Server { return &Server{} }
`})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.Scopes = []load.ScopeDecl{{
		Impl: graph.NewKey(m.ptr("example.com/mod/app", "Room"), ""), ImplType: m.ptr("example.com/mod/app", "Room"),
		Iface: graph.NewKey(m.typ("example.com/mod/app", "Rooms"), ""), IfaceType: m.typ("example.com/mod/app", "Rooms"),
		Pos: token.Position{Filename: "/mod/spec.go", Line: 11},
	}}
	msg := mustFail(t, in)
	if !strings.Contains(msg, "*example.com/mod/app.Room is scoped, but its constructor app.NewRoom takes a context.Context") || !strings.Contains(msg, "Init(ctx context.Context) error") {
		t.Errorf("diagnostic does not refuse the scoped constructor's ctx:\n%s", msg)
	}
}

// TestCapabilityNotes: a capability-named method with the wrong signature
// is never called, and a Close with no Stop is never released — said as a
// note, with generation going ahead.
func TestCapabilityNotes(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/comp", `package comp

import "context"

type Pool struct{}

func NewPool() *Pool        { return &Pool{} }
func (*Pool) Close() error { return nil }

type Conn struct{}

func NewConn() (*Conn, func(), error) { return &Conn{}, func() {}, nil }
func (*Conn) Close() error            { return nil }

type Consumer struct{}

func NewConsumer(p *Pool, c *Conn) *Consumer { return &Consumer{} }
func (*Consumer) Run() error                 { return nil }

type Server struct{}

func NewServer(c *Consumer) *Server                { return &Server{} }
func (*Server) Shutdown(ctx context.Context) error { return nil }
`})
	in := m.input("example.com/mod/comp", m.ptr("example.com/mod/comp", "Server"))
	r := mustResolve(t, in)
	var got []string
	for _, n := range r.Notes {
		got = append(got, n.String())
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"x.go:7:6: servo: note: *example.com/mod/comp.Pool has a Close method but no Stop(ctx context.Context) error, so servo will not release it at shutdown",
		"x.go:18:18: servo: note: *example.com/mod/comp.Consumer's Run method has the signature Run() error, not Run(ctx context.Context) error, so servo will never call it",
		"x.go:22:6: servo: note: *example.com/mod/comp.Server has a Shutdown method but no Stop",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes missing %q:\n%s", want, joined)
		}
	}
	// A cleanup func is how Conn is released, so its Close says nothing.
	if strings.Contains(joined, "comp.Conn") {
		t.Errorf("a provider with a cleanup func got a Close note:\n%s", joined)
	}
	if len(got) != 3 {
		t.Errorf("got %d notes, want 3:\n%s", len(got), joined)
	}

	in.OverrideVariant = true
	if r := mustResolve(t, in); len(r.Notes) != 0 {
		t.Errorf("the override variant reported notes: %v", r.Notes)
	}
}

// TestAliasInTypeArgumentResolves: Cache[DBAlias] and Cache[*DB] are one
// type, so a constructor for one satisfies a parameter written as the
// other.
func TestAliasInTypeArgumentResolves(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", `package app

type DB struct{}
type DBAlias = *DB

type Cache[T any] struct{}

func NewCache() *Cache[*DB] { return nil }

type Service struct{}

func NewService(c *Cache[DBAlias]) *Service { return &Service{} }
`})
	r := mustResolve(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Service")))
	if got := providerOf(t, r, m.ptr("example.com/mod/app", "Service")); got != "app.NewService" {
		t.Fatalf("Service provided by %s", got)
	}
}

// TestDeepChainCostsLinearMemory: the needed-by chain was copied twice per
// frame, so a 3,000-deep chain allocated about 790 MB. Frames are shared
// links now, and a missing provider at the bottom still prints every frame
// in order.
func TestDeepChainCostsLinearMemory(t *testing.T) {
	const depth = 2000
	var b strings.Builder
	b.WriteString("package app\n\ntype Missing struct{}\n\n")
	for i := 0; i < depth; i++ {
		dep := fmt.Sprintf("*T%d", i+1)
		if i == depth-1 {
			dep = "*Missing"
		}
		fmt.Fprintf(&b, "type T%d struct{}\n\nfunc NewT%d(d %s) *T%d { return nil }\n\n", i, i, dep, i)
	}
	m := checkModule(t, pkgSrc{"example.com/mod/app", b.String()})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "T0"))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, diags := Resolve(in)
	runtime.ReadMemStats(&after)

	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].Message
	if n := strings.Count(msg, "needed by"); n != depth {
		t.Errorf("chain has %d frames, want %d", n, depth)
	}
	if first, last := strings.Index(msg, "needed by *example.com/mod/app.T1999 "), strings.Index(msg, "needed by *example.com/mod/app.T0 "); first < 0 || last < first {
		t.Errorf("chain is not innermost-consumer first")
	}
	// The quadratic copy allocated hundreds of megabytes at this depth;
	// the bound leaves room for everything else Resolve allocates.
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("resolving a %d-deep chain allocated %.1f MB", depth, float64(allocated)/(1<<20))
	if allocated > 128<<20 {
		t.Errorf("resolving a %d-deep chain allocated %d MB", depth, allocated>>20)
	}
}
