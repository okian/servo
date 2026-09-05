package resolve

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
	"github.com/okian/servo/v3/internal/route"
)

// httpAppSrc is the HTTP-pass fixture: a served handler with a request
// struct and a dependency, a handler whose dependency has no provider, and
// a scoped type with an accessor so both directions of the capture rule
// can be exercised.
const httpAppSrc = `
package httpapp

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Repo struct{}
func NewRepo() *Repo { return &Repo{} }

func NewHTTPConfig() *servo.HTTPConfig { return &servo.HTTPConfig{} }

type OrderReq struct {
	Category string ` + "`path:\"category\"`" + `
}
type OrderResp struct{ ID string }

//servo:post /order/{category}
func Order(ctx context.Context, req *OrderReq, r *Repo) (servo.Json[*OrderResp], error) {
	return nil, nil
}

// Missing has no provider anywhere.
type Missing struct{}

//servo:get /lost
func Lost(ctx context.Context, m *Missing) (servo.Json[*OrderResp], error) { return nil, nil }

type SessionKey string
type Session struct{}
func NewSession(k SessionKey) *Session { return &Session{} }
func (_ *Session) ScopeKey(ctx context.Context) (SessionKey, error) { return "", nil }
type Sessions interface {
	Acquire(ctx context.Context) (*Session, func(), error)
}

//servo:get /me
func Me(ctx context.Context, s *Session) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:get /me/ok
func MeOK(ctx context.Context, s Sessions) (servo.Json[*OrderResp], error) { return nil, nil }
`

func checkHTTPFixture(t *testing.T) (*types.Package, *token.FileSet, []*packages.Package, []*graph.Provider, []*route.Route, *types.Package) {
	t.Helper()
	ctxPkg := loadPkg(t, "context")
	servoPkg := loadPkg(t, graph.ServoPackagePath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "httpapp.go", httpAppSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importerFor(ctxPkg, servoPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/httpapp", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "httpapp", PkgPath: "example.com/httpapp",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
		Module: &packages.Module{Path: "example.com", Main: true},
	}}
	accepted, _ := graph.ScanCandidates(pkgs, "example.com/httpapp")
	routes, rdiags := route.Scan(pkgs, servoPkg.Types)
	if len(rdiags) != 0 {
		t.Fatalf("fixture scan diagnostics: %v", rdiags)
	}
	return pkg, fset, pkgs, accepted, routes, servoPkg.Types
}

func routesNamed(t *testing.T, routes []*route.Route, names ...string) []*route.Route {
	t.Helper()
	var out []*route.Route
	for _, name := range names {
		found := false
		for _, rt := range routes {
			if rt.Name == name {
				out = append(out, rt)
				found = true
			}
		}
		if !found {
			t.Fatalf("no route for handler %s", name)
		}
	}
	return out
}

func httpInput(servoTypes *types.Package, rts []*route.Route) *HTTPInput {
	ck, ct := route.HTTPConfigKey(servoTypes)
	return &HTTPInput{
		Pos:        token.Position{Filename: "spec.go", Line: 12},
		Routes:     rts,
		ConfigKey:  ck,
		ConfigType: ct,
	}
}

func TestResolveHTTPHappyPath(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewRepo"),
		findHTTPProvider(t, all, "NewHTTPConfig"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Order")),
	}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if resolved.HTTP == nil {
		t.Fatalf("Resolved.HTTP is nil")
	}
	if resolved.HTTP.Config == nil || resolved.HTTP.Config.Key.String() != "*"+graph.ServoPackagePath+".HTTPConfig" {
		t.Fatalf("config node = %+v", resolved.HTTP.Config)
	}
	if len(resolved.HTTP.Routes) != 1 {
		t.Fatalf("got %d plan routes, want 1", len(resolved.HTTP.Routes))
	}
	hr := resolved.HTTP.Routes[0]
	if hr.Route.Name != "httpapp.Order" {
		t.Fatalf("plan route = %s", hr.Route.Name)
	}
	if len(hr.Deps) != 1 || hr.Deps[0].Key != ptrKey(pkg, "Repo") {
		t.Fatalf("plan deps = %+v", hr.Deps)
	}
	// The config and every handler dep are constructed like any other
	// singleton: they must be in Order, but never in Roots — Roots keeps
	// meaning "declared servo.Root".
	inOrder := map[graph.Key]bool{}
	for _, n := range resolved.Order {
		inOrder[n.Key] = true
	}
	if !inOrder[hr.Deps[0].Key] || !inOrder[resolved.HTTP.Config.Key] {
		t.Fatalf("HTTP nodes missing from Order: %v", resolved.Order)
	}
	if len(resolved.Roots) != 0 {
		t.Fatalf("Roots = %v, want none", resolved.Roots)
	}
}

func TestResolveHTTPMissingConfig(t *testing.T) {
	_, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{findHTTPProvider(t, all, "NewRepo")} // no NewHTTPConfig
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Order")),
	}

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) == 0 {
		t.Fatalf("resolved=%v diags=%v, want a missing-config diagnostic", resolved, diags)
	}
	text := diagText(diags)
	for _, want := range []string{
		"no provider for *" + graph.ServoPackagePath + ".HTTPConfig",
		"needed by HTTP server (servo.HTTP())",
		"spec.go:12",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, text)
		}
	}
}

func TestResolveHTTPMissingHandlerDep(t *testing.T) {
	_, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{findHTTPProvider(t, all, "NewHTTPConfig")}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Lost")),
	}

	_, diags := Resolve(in)
	text := diagText(diags)
	for _, want := range []string{
		"no provider for *example.com/httpapp.Missing",
		"needed by handler httpapp.Lost (GET /lost)",
		// The unresolved key is a pointer to a struct in the request
		// position, so the hint about binding tags has to be there — this
		// is the mistake of writing a request struct and forgetting its
		// tags, and "no provider" alone sends the user to the wrong fix.
		"meant as the request struct",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, text)
		}
	}
}

func TestResolveHTTPScopedDepRejected(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	in := Input{
		Spec: &load.Spec{Scopes: []load.ScopeDecl{{
			Impl: ptrKey(pkg, "Session"), ImplType: ptrType(pkg, "Session"),
			Iface: namedKey(pkg, "Sessions"), IfaceType: namedType(pkg, "Sessions"),
			Pos: token.Position{Filename: "spec.go", Line: 10},
		}}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Me")),
	}

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) == 0 {
		t.Fatalf("resolved=%v diags=%v, want a scoped-dependency diagnostic", resolved, diags)
	}
	text := diagText(diags)
	for _, want := range []string{
		"*example.com/httpapp.Session is scoped",
		"handler httpapp.Me (GET /me)",
		"example.com/httpapp.Sessions",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, text)
		}
	}
}

// The accessor interface is the documented way a handler reaches scoped
// state, and it must resolve as an ordinary dependency.
func TestResolveHTTPAccessorDepAllowed(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	in := Input{
		Spec: &load.Spec{Scopes: []load.ScopeDecl{{
			Impl: ptrKey(pkg, "Session"), ImplType: ptrType(pkg, "Session"),
			Iface: namedKey(pkg, "Sessions"), IfaceType: namedType(pkg, "Sessions"),
			Pos: token.Position{Filename: "spec.go", Line: 10},
		}}},
		Candidates: all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.MeOK")),
	}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	hr := resolved.HTTP.Routes[0]
	if len(hr.Deps) != 1 || hr.Deps[0].Kind != NodeScopeAccessor {
		t.Fatalf("deps = %+v, want the Sessions accessor node", hr.Deps)
	}
}

// A nil HTTP input must leave resolution byte-for-byte as before — the
// no-HTTP invariant starts here.
func TestResolveNilHTTPInput(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewLogger"),
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Logger"), Type: ptrType(pkg, "Logger"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	resolved, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if resolved.HTTP != nil {
		t.Fatalf("Resolved.HTTP = %+v, want nil", resolved.HTTP)
	}
}

func findHTTPProvider(t *testing.T, providers []*graph.Provider, name string) *graph.Provider {
	t.Helper()
	for _, p := range providers {
		if p.Name == "httpapp."+name {
			return p
		}
	}
	t.Fatalf("no provider named httpapp.%s among %d providers", name, len(providers))
	return nil
}
