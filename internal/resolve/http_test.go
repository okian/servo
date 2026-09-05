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
	"net/http"

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

type User struct{ Name string }

func NewUser() *User { return &User{} }

type UserExtractor struct{}
func NewUserExtractor() *UserExtractor { return &UserExtractor{} }
func (e *UserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

// BadExtractor's method has the wrong parameter list.
type BadExtractor struct{}
func NewBadExtractor() *BadExtractor { return &BadExtractor{} }
func (e *BadExtractor) Extract(name string) (*User, error) { return nil, nil }

type Recover struct{}
func NewRecover() *Recover { return &Recover{} }
func (m *Recover) Middleware(next http.Handler) http.Handler { return next }

type NotMiddleware struct{}
func NewNotMiddleware() *NotMiddleware { return &NotMiddleware{} }

//servo:get /ping
func Ping(ctx context.Context) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:get /me2
func Me2(ctx context.Context, u *User) (servo.Json[*OrderResp], error) { return nil, nil }

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*OrderResp], error) { return nil, nil }
`

func checkHTTPFixture(t *testing.T) (*types.Package, *token.FileSet, []*packages.Package, []*graph.Provider, []*route.Route, *types.Package) {
	t.Helper()
	ctxPkg := loadPkg(t, "context")
	httpPkg := loadPkg(t, "net/http")
	servoPkg := loadPkg(t, graph.ServoPackagePath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "httpapp.go", httpAppSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importerFor(ctxPkg, httpPkg, servoPkg)}
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
	if len(hr.Args) != 1 || hr.Args[0].Node == nil || hr.Args[0].Node.Key != ptrKey(pkg, "Repo") {
		t.Fatalf("plan args = %+v", hr.Args)
	}
	// The config and every handler dep are constructed like any other
	// singleton: they must be in Order, but never in Roots — Roots keeps
	// meaning "declared servo.Root".
	inOrder := map[graph.Key]bool{}
	for _, n := range resolved.Order {
		inOrder[n.Key] = true
	}
	if !inOrder[hr.Args[0].Node.Key] || !inOrder[resolved.HTTP.Config.Key] {
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
	if len(hr.Args) != 1 || hr.Args[0].Node == nil || hr.Args[0].Node.Kind != NodeScopeAccessor {
		t.Fatalf("args = %+v, want the Sessions accessor node", hr.Args)
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

// With no Groups declared, a telemetry-tagged route is simply not this
// injector's to serve; declaring the group brings it in.
func TestResolveHTTPFiltersUndeclaredGroups(t *testing.T) {
	_, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{findHTTPProvider(t, all, "NewHTTPConfig")}
	build := func(groups []load.GroupDecl) *Resolved {
		in := Input{
			Spec:       &load.Spec{},
			Candidates: candidates,
			Caps:       graph.EmptyCapabilities(),
			Scope:      map[string]bool{"example.com/httpapp": true},
			Fset:       fset,
			Pkgs:       pkgs,
			HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Healthz")),
		}
		in.HTTP.Groups = groups
		resolved, diags := Resolve(in)
		if len(diags) > 0 {
			t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
		}
		return resolved
	}

	without := build(nil)
	if len(without.HTTP.Routes) != 0 || len(without.HTTP.Groups) != 0 {
		t.Fatalf("undeclared group still served: %+v", without.HTTP)
	}
	with := build([]load.GroupDecl{{Name: "telemetry", Pos: token.Position{Filename: "spec.go", Line: 13}}})
	if len(with.HTTP.Routes) != 1 || with.HTTP.Routes[0].Route.Group != "telemetry" {
		t.Fatalf("declared group not served: %+v", with.HTTP.Routes)
	}
	if len(with.HTTP.Groups) != 1 || with.HTTP.Groups[0] != "telemetry" {
		t.Fatalf("plan groups = %v", with.HTTP.Groups)
	}
}

func TestResolveHTTPExtractor(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewUserExtractor"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Me2")),
	}
	in.HTTP.Extracts = []load.ExtractDecl{{
		Type: ptrKey(pkg, "UserExtractor"), TypeT: ptrType(pkg, "UserExtractor"),
		Pos: token.Position{Filename: "spec.go", Line: 20},
	}}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.HTTP.Extractors) != 1 || resolved.HTTP.Extractors[0].Produces != ptrKey(pkg, "User") {
		t.Fatalf("extractors = %+v", resolved.HTTP.Extractors)
	}
	hr := resolved.HTTP.Routes[0]
	if len(hr.Args) != 1 || hr.Args[0].Extractor == nil || hr.Args[0].Node != nil {
		t.Fatalf("args = %+v, want one extracted arg", hr.Args)
	}
	// The extractor node itself is an ordinary singleton in Order.
	found := false
	for _, n := range resolved.Order {
		if n.Key == ptrKey(pkg, "UserExtractor") {
			found = true
		}
	}
	if !found {
		t.Fatalf("extractor node missing from Order")
	}
}

func TestResolveHTTPRejectsBadExtractor(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewBadExtractor"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Ping")),
	}
	in.HTTP.Extracts = []load.ExtractDecl{{
		Type: ptrKey(pkg, "BadExtractor"), TypeT: ptrType(pkg, "BadExtractor"),
		Pos: token.Position{Filename: "spec.go", Line: 20},
	}}

	_, diags := Resolve(in)
	text := diagText(diags)
	if !strings.Contains(text, "must have a method Extract(r *http.Request) (T, error)") {
		t.Fatalf("diagnostics:\n%s", text)
	}
}

func TestResolveHTTPRejectsExtractedAndProvided(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewUserExtractor"),
		findHTTPProvider(t, all, "NewUser"), // the conflict
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Me2")),
	}
	in.HTTP.Extracts = []load.ExtractDecl{{
		Type: ptrKey(pkg, "UserExtractor"), TypeT: ptrType(pkg, "UserExtractor"),
		Pos: token.Position{Filename: "spec.go", Line: 20},
	}}

	_, diags := Resolve(in)
	text := diagText(diags)
	if !strings.Contains(text, "never both") || !strings.Contains(text, "httpapp.NewUser") {
		t.Fatalf("diagnostics:\n%s", text)
	}
}

func TestResolveHTTPMiddleware(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewRecover"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Ping")),
	}
	in.HTTP.Uses = []load.UseDecl{{
		Type: ptrKey(pkg, "Recover"), TypeT: ptrType(pkg, "Recover"),
		Routes: []load.RouteSel{{Pattern: "GET /ping", Pos: token.Position{Filename: "spec.go", Line: 14}}},
		Pos:    token.Position{Filename: "spec.go", Line: 14},
	}}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.HTTP.Uses) != 1 || resolved.HTTP.Uses[0].Node.Key != ptrKey(pkg, "Recover") {
		t.Fatalf("uses = %+v", resolved.HTTP.Uses)
	}
}

func TestResolveHTTPRejectsNonMiddleware(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewNotMiddleware"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Ping")),
	}
	in.HTTP.Uses = []load.UseDecl{{
		Type: ptrKey(pkg, "NotMiddleware"), TypeT: ptrType(pkg, "NotMiddleware"),
		Pos: token.Position{Filename: "spec.go", Line: 14},
	}}

	_, diags := Resolve(in)
	text := diagText(diags)
	if !strings.Contains(text, "must have a method Middleware(next http.Handler) http.Handler") {
		t.Fatalf("diagnostics:\n%s", text)
	}
}

func TestResolveHTTPRejectsUnknownUseSelectors(t *testing.T) {
	pkg, fset, pkgs, all, routes, servoTypes := checkHTTPFixture(t)
	candidates := []*graph.Provider{
		findHTTPProvider(t, all, "NewHTTPConfig"),
		findHTTPProvider(t, all, "NewRecover"),
	}
	in := Input{
		Spec:       &load.Spec{},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpapp": true},
		Fset:       fset,
		Pkgs:       pkgs,
		HTTP:       httpInput(servoTypes, routesNamed(t, routes, "httpapp.Ping")),
	}
	in.HTTP.Uses = []load.UseDecl{
		{
			Type: ptrKey(pkg, "Recover"), TypeT: ptrType(pkg, "Recover"),
			Groups: []string{"nope"},
			Pos:    token.Position{Filename: "spec.go", Line: 14},
		},
		{
			Type: ptrKey(pkg, "Recover"), TypeT: ptrType(pkg, "Recover"),
			Routes: []load.RouteSel{{Pattern: "POST /missing", Pos: token.Position{Filename: "spec.go", Line: 15}}},
			Pos:    token.Position{Filename: "spec.go", Line: 15},
		},
	}

	_, diags := Resolve(in)
	text := diagText(diags)
	if !strings.Contains(text, `selects group "nope"`) || !strings.Contains(text, "matches no served route") {
		t.Fatalf("diagnostics:\n%s", text)
	}
}
