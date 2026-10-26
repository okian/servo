package render

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// checkRenderFixture type-checks src as example.com/app, with context
// importable, and returns its package, its candidate index and a lookup.
func checkRenderFixture(t *testing.T, src string) (*packages.Package, []*graph.Provider, func(string) types.Type) {
	t.Helper()
	ctxPkg := loadContextPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: singlePkgImporter{ctxPkg.Types}}).Check("example.com/app", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	appPkg := &packages.Package{
		Name: "app", PkgPath: "example.com/app",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")
	return appPkg, candidates, func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
}

const configFixtureSrc = `
package app

type Flags struct{ Verbose bool }

type settings struct{ dsn string }

type Server struct{}
func NewServer(f *Flags, s settings) *Server { return &Server{} }
`

// TestToGraphReportsAConfigByItsLoader: a //servo:config node has no
// provider; it is built by the loader generated into its package, which is
// what Provider names, and it sits at level 0 after the supplied values
// and before anything the app constructs.
func TestToGraphReportsAConfigByItsLoader(t *testing.T) {
	appPkg, candidates, lookup := checkRenderFixture(t, configFixtureSrc)
	flags, server := types.NewPointer(lookup("Flags")), types.NewPointer(lookup("Server"))
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec: &load.Spec{
			Roots:  []load.RootDecl{{Key: graph.NewKey(server, ""), Type: server, Pos: token.Position{Filename: "spec.go", Line: 5}}},
			Values: []load.ValueDecl{{Key: graph.NewKey(flags, ""), Type: flags, Pos: token.Position{Filename: "spec.go", Line: 6}}},
		},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs: []*graph.ConfigDecl{{
			Key: graph.NewKey(lookup("settings"), ""), Type: lookup("settings"), TypeName: "settings",
			Prefix: "APP", Section: "app", PkgPath: "example.com/app", PkgName: "app",
			Pos: token.Position{Filename: "app.go", Line: 6, Column: 6},
		}},
		Pkgs: []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	g := ToGraph(resolved, nil)
	var order []string
	for _, n := range g.Nodes {
		order = append(order, n.Type)
	}
	if want := "*example.com/app.Flags,example.com/app.settings,*example.com/app.Server"; strings.Join(order, ",") != want {
		t.Fatalf("nodes = %v, want the supplied value, then the config, then what the app builds", order)
	}
	cfg := g.Nodes[1]
	if cfg.Level != 0 || cfg.Binding != "config directive" || cfg.Provider != "app.ServoConfig" || cfg.Pos != "app.go:6:6" {
		t.Errorf("config node = %+v, want level 0, bound by its directive, built by app.ServoConfig, at its declaration", cfg)
	}
	// The generated Graph() names the loader too; only the position goes.
	if c := Committed(resolved).Nodes[1]; c.Provider != "app.ServoConfig" || c.Pos != "" {
		t.Errorf("committed config node = %+v, want the loader named and no position", c)
	}
}

const borrowOrderFixtureSrc = `
package app

import "context"

type RoomKey string

type Clock struct{}
func NewClock() *Clock { return &Clock{} }

type Store struct{}
func NewStore(c *Clock) *Store { return &Store{} }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

// Room takes the deeper of its two borrows first.
type Room struct{}
func NewRoom(k RoomKey, s *Store, c *Clock) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Server struct{}
func NewServer(r Rooms) *Server { return &Server{} }
`

// TestScopeBorrowsAreListedByLevel: the borrows are the singletons a scope
// shares, listed in the order the generated header lists them and the
// scope's teardown is computed from — by level, shallowest first, not in
// the order a member happens to take them.
func TestScopeBorrowsAreListedByLevel(t *testing.T) {
	appPkg, candidates, lookup := checkRenderFixture(t, borrowOrderFixtureSrc)
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec: &load.Spec{
			Roots: []load.RootDecl{{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: "spec.go", Line: 5}}},
			Scopes: []load.ScopeDecl{{
				Impl: graph.NewKey(ptr("Room"), ""), ImplType: ptr("Room"),
				Iface: graph.NewKey(lookup("Rooms"), ""), IfaceType: lookup("Rooms"),
				Pos: token.Position{Filename: "spec.go", Line: 6},
			}},
		},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Fset:       appPkg.Fset,
		Pkgs:       []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}

	g := ToGraph(resolved, nil)
	if len(g.Scopes) != 1 {
		t.Fatalf("got %d scopes, want 1", len(g.Scopes))
	}
	if got, want := strings.Join(g.Scopes[0].Borrows, ","), "*example.com/app.Clock,*example.com/app.Store"; got != want {
		t.Errorf("borrows = %s, want %s: Clock is level 1 and Store, which takes it, level 2", got, want)
	}
}
