package resolve

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

const configAppSrc = `
package app

type settings struct{ dsn string }

type Server struct{}
func NewServer(s settings) *Server { return &Server{} }

// redisSettings exists so two configs can be used at once and collide on
// an env name when a test says so.
type redisSettings struct{ addr string }

type Cache struct{}
func NewCache(s redisSettings) *Cache { return &Cache{} }

// conflicted carries a directive in the tests AND has a constructor —
// the contradiction configProviderDiagnostic reports.
type conflicted struct{}
func NewConflicted() conflicted { return conflicted{} }

type Holder struct{}
func NewHolder(c conflicted) *Holder { return &Holder{} }

// Standalone needs no config at all, for the test where a ConfigFile is
// declared and nothing would read it.
type Standalone struct{}
func NewStandalone() *Standalone { return &Standalone{} }
`

func checkConfigFixture(t *testing.T) (*types.Package, []*graph.Provider) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", configAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	// The injector package is the fixture's own, so unexported constructors
	// and unexported config types are in play exactly as the feature
	// intends them to be.
	accepted, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/app")
	return pkg, accepted
}

// configDecl builds the ConfigDecl the scanner would produce for a fixture
// type, with one string field per name given.
func configDecl(pkg *types.Package, typeName, prefix string, line int, fieldNames ...string) *graph.ConfigDecl {
	d := &graph.ConfigDecl{
		Key:      namedKey(pkg, typeName),
		Type:     namedType(pkg, typeName),
		TypeName: typeName,
		Prefix:   prefix,
		Section:  strings.ToLower(prefix),
		PkgPath:  "example.com/app",
		PkgName:  "app",
		Dir:      "app",
		Pos:      token.Position{Filename: "app.go", Line: line},
	}
	for i, name := range fieldNames {
		d.Fields = append(d.Fields, graph.ConfigField{
			FieldName: name,
			Name:      name,
			EnvName:   prefix + "_" + strings.ToUpper(name),
			FileKey:   name,
			Kind:      graph.KindString,
			Pos:       token.Position{Filename: "app.go", Line: line + 1 + i},
		})
	}
	return d
}

func TestConfigResolvesToGeneratedLoader(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	decl := configDecl(pkg, "settings", "APP", 4, "dsn")
	resolved, diags := Resolve(Input{
		Spec:       &load.Spec{Roots: []load.RootDecl{rootDecl(pkg, "Server")}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{decl},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}

	if len(resolved.Configs) != 1 {
		t.Fatalf("got %d configs, want 1", len(resolved.Configs))
	}
	cn := resolved.Configs[0]
	if cn.Kind != NodeConfig || cn.Config != decl || cn.Level != 0 || cn.Binding != "config directive" {
		t.Fatalf("config node = kind %v config %v level %d binding %q", cn.Kind, cn.Config, cn.Level, cn.Binding)
	}
	// Out of Order, like a supplied value: nothing in the construction
	// loop builds it, the preamble does.
	for _, n := range resolved.Order {
		if n == cn {
			t.Fatal("config node must not appear in Resolved.Order")
		}
	}
	if resolved.ByKey[decl.Key] != cn {
		t.Fatal("config node missing from ByKey — explain/why could not find it")
	}

	server := resolved.ByKey[ptrKey(pkg, "Server")]
	if server == nil || len(server.Deps) != 1 || server.Deps[0] != cn {
		t.Fatalf("server deps = %v", server.Deps)
	}
	if server.Level != 1 {
		t.Fatalf("server level = %d, want 1 (config sits at level 0)", server.Level)
	}
}

func TestConfigUnusedIsNotEmitted(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	// The redis config exists module-wide but this graph never asks for it.
	decl := configDecl(pkg, "redisSettings", "REDIS", 9, "addr")
	appDecl := configDecl(pkg, "settings", "APP", 4, "dsn")
	resolved, diags := Resolve(Input{
		Spec:       &load.Spec{Roots: []load.RootDecl{rootDecl(pkg, "Server")}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{appDecl, decl},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Configs) != 1 || resolved.Configs[0].Config != appDecl {
		t.Fatalf("configs = %v, want only the used one", resolved.Configs)
	}
}

func TestConfigProviderConflictDiagnostic(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	decl := configDecl(pkg, "conflicted", "CONF", 15, "x")
	_, diags := Resolve(Input{
		Spec:       &load.Spec{Roots: []load.RootDecl{rootDecl(pkg, "Holder")}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{decl},
	})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "carries a //servo:config directive") {
		t.Fatalf("diags = \n%s", diagText(diags))
	}
	if !strings.Contains(diags[0].Message, "app.NewConflicted") {
		t.Fatalf("diagnostic does not name the constructor:\n%s", diags[0].Message)
	}
}

func TestConfigValueWins(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	decl := configDecl(pkg, "settings", "APP", 4, "dsn")
	resolved, diags := Resolve(Input{
		Spec: &load.Spec{
			Roots:  []load.RootDecl{rootDecl(pkg, "Server")},
			Values: []load.ValueDecl{{Key: namedKey(pkg, "settings"), Type: namedType(pkg, "settings"), Pos: token.Position{Filename: "spec.go", Line: 11}}},
		},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{decl},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	// servo.Value means "the caller supplies this" — precedence rule 1 —
	// so the config loader is not consulted and not emitted.
	if len(resolved.Supplied) != 1 || len(resolved.Configs) != 0 {
		t.Fatalf("supplied = %d configs = %d, want 1 and 0", len(resolved.Supplied), len(resolved.Configs))
	}
}

func TestConfigEnvCollisionDiagnostic(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	// Same prefix, same field name, two types — both used by the graph.
	a := configDecl(pkg, "settings", "APP", 4, "dsn")
	b := configDecl(pkg, "redisSettings", "APP", 9, "dsn")
	_, diags := Resolve(Input{
		Spec: &load.Spec{Roots: []load.RootDecl{
			rootDecl(pkg, "Server"),
			rootDecl(pkg, "Cache"),
		}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{a, b},
	})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "same environment variable APP_DSN") {
		t.Fatalf("diags = \n%s", diagText(diags))
	}
}

func TestConfigFileKeyCollisionOnlyWithConfigFile(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	// Different prefixes (no env collision), but the section key is forced
	// to collide — only meaningful once a file is declared.
	a := configDecl(pkg, "settings", "APP", 4, "dsn")
	b := configDecl(pkg, "redisSettings", "REDIS", 9, "dsn")
	b.Section = "app"
	spec := &load.Spec{Roots: []load.RootDecl{rootDecl(pkg, "Server"), rootDecl(pkg, "Cache")}}
	input := Input{
		Spec:       spec,
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{a, b},
	}

	if _, diags := Resolve(input); len(diags) != 0 {
		t.Fatalf("env-only graph must not check file keys:\n%s", diagText(diags))
	}

	spec.ConfigFile = &load.ConfigFileDecl{Path: "config.yaml", Pos: token.Position{Filename: "spec.go", Line: 12}}
	if _, diags := Resolve(input); len(diags) != 1 || !strings.Contains(diags[0].Message, "same config file key app.dsn") {
		t.Fatalf("diags = \n%s", diagText(diags))
	}
}

func TestConfigFileUnusedDiagnostic(t *testing.T) {
	pkg, all := checkConfigFixture(t)
	decl := configDecl(pkg, "redisSettings", "REDIS", 9, "addr")
	_, diags := Resolve(Input{
		Spec: &load.Spec{
			Roots:      []load.RootDecl{rootDecl(pkg, "Standalone")},
			ConfigFile: &load.ConfigFileDecl{Path: "config.yaml", Pos: token.Position{Filename: "spec.go", Line: 12}},
		},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Configs:    []*graph.ConfigDecl{decl},
	})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, `servo.ConfigFile("config.yaml") is declared, but no //servo:config type is in this graph`) {
		t.Fatalf("diags = \n%s", diagText(diags))
	}
}

// TestConfigInsideScopeIsRefused pins the v1 limitation with its
// workaround: a scoped constructor cannot borrow a config value, because
// configs live as locals in New rather than App fields.
func TestConfigInsideScopeIsRefused(t *testing.T) {
	pkg, fset, pkgs, all := checkConfigScopeFixture(t)
	decl := &graph.ConfigDecl{
		Key:      namedKey(pkg, "settings"),
		Type:     namedType(pkg, "settings"),
		TypeName: "settings",
		Prefix:   "APP",
		Section:  "app",
		PkgPath:  "example.com/scopedcfg",
		PkgName:  "scopedcfg",
		Dir:      "scopedcfg",
		Pos:      token.Position{Filename: "scopedcfg.go", Line: 8},
		Fields:   []graph.ConfigField{{FieldName: "dsn", Name: "dsn", EnvName: "APP_DSN", FileKey: "dsn", Kind: graph.KindString}},
	}
	scope := map[string]bool{}
	for _, c := range all {
		scope[c.Pkg] = true
	}
	_, diags := Resolve(Input{
		Spec: &load.Spec{
			Roots: []load.RootDecl{rootDecl(pkg, "Server")},
			Scopes: []load.ScopeDecl{{
				Impl: ptrKey(pkg, "Room"), ImplType: ptrType(pkg, "Room"),
				Iface: namedKey(pkg, "Rooms"), IfaceType: namedType(pkg, "Rooms"),
				Pos: token.Position{Filename: "spec.go", Line: 10},
			}},
		},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      scope,
		Configs:    []*graph.ConfigDecl{decl},
		Fset:       fset,
		Pkgs:       pkgs,
	})
	if len(diags) == 0 {
		t.Fatal("expected the scoped-config diagnostic")
	}
	if !strings.Contains(diags[0].Message, "scoped constructor depends on config type") ||
		!strings.Contains(diags[0].Message, "singleton of your own") {
		t.Fatalf("diags = \n%s", diagText(diags))
	}
}

const configScopedSrc = `
package scopedcfg

import "context"

type RoomKey string

type settings struct{ dsn string }

type Room struct{}
func NewRoom(k RoomKey, s settings) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}
func NewServer(r Rooms) *Server { return &Server{} }
`

func checkConfigScopeFixture(t *testing.T) (*types.Package, *token.FileSet, []*packages.Package, []*graph.Provider) {
	t.Helper()
	ctxPkg := loadPkg(t, "context")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "scopedcfg.go", configScopedSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importerFor(ctxPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/scopedcfg", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "scopedcfg", PkgPath: "example.com/scopedcfg",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}}
	accepted, _ := graph.ScanCandidates(pkgs, "example.com/scopedcfg")
	return pkg, fset, pkgs, accepted
}
