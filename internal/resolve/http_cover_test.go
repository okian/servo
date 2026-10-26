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

// httpCoverSrc holds, beside a few ordinary handlers, every wrong shape
// an extractor, a middleware or a request struct can take, and handlers
// whose dependencies fail in the positions the request-struct hint has to
// tell apart.
const httpCoverSrc = `
package httpcov

import (
	"context"
	"net/http"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{ OK bool }

func NewHTTPConfig() *servo.HTTPConfig { return &servo.HTTPConfig{} }

type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

// Missing has no constructor anywhere.
type Missing struct{}

type Mode string

type Store interface{ Get() string }

type User struct{ Name string }

type UserExtractor struct{}

func NewUserExtractor() *UserExtractor                         { return &UserExtractor{} }
func (*UserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

// OtherUserExtractor produces the type UserExtractor does.
type OtherUserExtractor struct{}

func NewOtherUserExtractor() *OtherUserExtractor                    { return &OtherUserExtractor{} }
func (*OtherUserExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

// NoErrExtractor's Extract forgot its error result.
type NoErrExtractor struct{}

func NewNoErrExtractor() *NoErrExtractor              { return &NoErrExtractor{} }
func (*NoErrExtractor) Extract(r *http.Request) *User { return &User{} }

// UnbuiltExtractor has the method and no constructor.
type UnbuiltExtractor struct{}

func (*UnbuiltExtractor) Extract(r *http.Request) (*User, error) { return &User{}, nil }

// Token is a request struct an extractor also produces.
type Token struct {
	Value string ` + "`header:\"Authorization\"`" + `
}

type TokenExtractor struct{}

func NewTokenExtractor() *TokenExtractor                         { return &TokenExtractor{} }
func (*TokenExtractor) Extract(r *http.Request) (*Token, error) { return &Token{}, nil }

// Settings is a request struct whose pointee a //servo:config loads.
type Settings struct {
	Admin bool ` + "`json:\"admin\"`" + `
}

// Wrap is a middleware function type, the shape otelhttp ships; BadWrap
// is a function type of some other shape.
type Wrap func(http.Handler) http.Handler

func NewWrap() Wrap { return func(h http.Handler) http.Handler { return h } }

type BadWrap func(string) string

func NewBadWrap() BadWrap { return func(s string) string { return s } }

type ParamMiddleware struct{}

func NewParamMiddleware() *ParamMiddleware                   { return &ParamMiddleware{} }
func (*ParamMiddleware) Middleware(next func()) http.Handler { return nil }

type ResultMiddleware struct{}

func NewResultMiddleware() *ResultMiddleware                            { return &ResultMiddleware{} }
func (*ResultMiddleware) Middleware(next http.Handler) http.HandlerFunc { return nil }

type UnbuiltMiddleware struct{}

func (*UnbuiltMiddleware) Middleware(next http.Handler) http.Handler { return next }

// Session is scoped, and SessionLog is in its scope only because it takes
// the key: no accessor hands it out.
type SessionKey string

type SessionLog struct{}

func NewSessionLog(k SessionKey) *SessionLog { return &SessionLog{} }

type Session struct{}

func NewSession(k SessionKey, l *SessionLog) *Session              { return &Session{} }
func (*Session) ScopeKey(ctx context.Context) (SessionKey, error) { return "", nil }

type Sessions interface {
	Acquire(ctx context.Context) (*Session, func(), error)
}

// JoinReq is a request struct that is also declared as a scoped type.
type JoinReq struct {
	Room string ` + "`path:\"room\"`" + `
}

func NewJoinReq(k SessionKey) *JoinReq                            { return &JoinReq{} }
func (*JoinReq) ScopeKey(ctx context.Context) (SessionKey, error) { return "", nil }

type JoinReqs interface {
	Acquire(ctx context.Context) (*JoinReq, func(), error)
}

type OrderReq struct {
	ID string ` + "`path:\"id\"`" + `
}

//servo:get /me
func Me(ctx context.Context, r *Repo, u *User) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /log
func ReadLog(ctx context.Context, l *SessionLog) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /lost/a
func LostA(ctx context.Context, m *Missing) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /lost/b
func LostB(ctx context.Context, m *Missing) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order/{id}
func Order(ctx context.Context, req *OrderReq, m *Missing) (servo.Json[*Resp], error) {
	return nil, nil
}

//servo:get /second
func Second(ctx context.Context, r *Repo, m *Missing) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /iface
func Iface(ctx context.Context, s Store) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /count
func Count(ctx context.Context, n *int) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /mode
func ModeOf(ctx context.Context, m *Mode) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /join/{room}
func Join(ctx context.Context, req *JoinReq) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /token
func Authorize(ctx context.Context, req *Token) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /settings
func Configure(ctx context.Context, req *Settings) (servo.Json[*Resp], error) { return nil, nil }
`

// httpCoverFixture is httpCoverSrc checked against the real servo and
// net/http packages, so the extractor and middleware shape checks see the
// types they look for.
type httpCoverFixture struct {
	pkg        *types.Package
	fset       *token.FileSet
	pkgs       []*packages.Package
	all        []*graph.Provider
	routes     []*route.Route
	servoTypes *types.Package
}

func checkHTTPCoverFixture(t *testing.T) *httpCoverFixture {
	t.Helper()
	ctxPkg := loadPkg(t, "context")
	httpPkg := loadPkg(t, "net/http")
	servoPkg := loadPkg(t, graph.ServoPackagePath)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "httpcov.go", httpCoverSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: importerFor(ctxPkg, httpPkg, servoPkg)}).Check("example.com/httpcov", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "httpcov", PkgPath: "example.com/httpcov",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
		Module: &packages.Module{Path: "example.com", Main: true},
	}}
	all, _ := graph.ScanCandidates(pkgs, "example.com/httpcov")
	routes, rdiags := route.Scan(pkgs, servoPkg.Types)
	if len(rdiags) != 0 {
		t.Fatalf("fixture scan diagnostics: %v", rdiags)
	}
	return &httpCoverFixture{pkg: pkg, fset: fset, pkgs: pkgs, all: all, routes: routes, servoTypes: servoPkg.Types}
}

// typ resolves a fixture type written the way a test reads it: "Wrap", or
// "*Repo" for a pointer to one.
func (f *httpCoverFixture) typ(expr string) types.Type {
	if name, ok := strings.CutPrefix(expr, "*"); ok {
		return types.NewPointer(f.pkg.Scope().Lookup(name).Type())
	}
	return f.pkg.Scope().Lookup(expr).Type()
}

// scope is servo.Scoped[*impl, iface]().
func (f *httpCoverFixture) scope(impl, iface string) load.ScopeDecl {
	return load.ScopeDecl{
		Impl: graph.NewKey(f.typ("*"+impl), ""), ImplType: f.typ("*" + impl),
		Iface: graph.NewKey(f.typ(iface), ""), IfaceType: f.typ(iface),
		Pos: token.Position{Filename: "spec.go", Line: 10},
	}
}

// input serves the named handlers ("httpcov.Me") with every fixture
// constructor as a candidate.
func (f *httpCoverFixture) input(t *testing.T, handlers ...string) Input {
	t.Helper()
	var rts []*route.Route
	if len(handlers) > 0 {
		rts = routesNamed(t, f.routes, handlers...)
	}
	return Input{
		Spec:       &load.Spec{},
		Candidates: f.all,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/httpcov": true},
		Fset:       f.fset,
		Pkgs:       f.pkgs,
		HTTP:       httpInput(f.servoTypes, rts),
	}
}

// extract and use are servo.Extract[T]() and servo.Use[T]() for the
// fixture type expr names.
func (f *httpCoverFixture) extract(expr string) load.ExtractDecl {
	return load.ExtractDecl{Type: graph.NewKey(f.typ(expr), ""), TypeT: f.typ(expr), Pos: token.Position{Filename: "spec.go", Line: 20}}
}

func (f *httpCoverFixture) use(expr string) load.UseDecl {
	return load.UseDecl{Type: graph.NewKey(f.typ(expr), ""), TypeT: f.typ(expr), Pos: token.Position{Filename: "spec.go", Line: 14}}
}

// TestHTTPDeclarationsWithTheWrongShape: every servo.Extract and servo.Use
// a generated server could not call is refused with what is wrong with it,
// rather than surfacing as a type error inside the generated file — and a
// declaration whose type cannot be built at all says only that.
func TestHTTPDeclarationsWithTheWrongShape(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	for _, tc := range []struct {
		name     string
		extracts []string
		uses     []string
		groups   []string
		useGroup string
		scoped   bool
		want     string
	}{
		{name: "extractor nothing constructs", extracts: []string{"*UnbuiltExtractor"},
			want: "servo: no provider for *example.com/httpcov.UnbuiltExtractor\n  needed by extractor *example.com/httpcov.UnbuiltExtractor (servo.Extract)"},
		{name: "scoped extractor", extracts: []string{"*Session"}, scoped: true,
			want: "servo: *example.com/httpcov.Session is scoped, but servo.Extract needs a singleton — the extractor is resolved once and its method called per request"},
		{name: "two extractors of one type", extracts: []string{"*UserExtractor", "*OtherUserExtractor"},
			want: "servo: two extractors produce *example.com/httpcov.User — *example.com/httpcov.UserExtractor and *example.com/httpcov.OtherUserExtractor; a handler parameter type maps to exactly one extractor"},
		{name: "extractor without Extract", extracts: []string{"*Repo"},
			want: "servo: *example.com/httpcov.Repo must have a method Extract(r *http.Request) (T, error) to be declared with servo.Extract — it has no Extract method"},
		{name: "Extract without its error", extracts: []string{"*NoErrExtractor"},
			want: "servo: *example.com/httpcov.NoErrExtractor must have a method Extract(r *http.Request) (T, error) to be declared with servo.Extract — its results must be exactly (T, error)"},
		{name: "middleware nothing constructs", uses: []string{"*UnbuiltMiddleware"},
			want: "servo: no provider for *example.com/httpcov.UnbuiltMiddleware\n  needed by middleware *example.com/httpcov.UnbuiltMiddleware (servo.Use)"},
		{name: "scoped middleware", uses: []string{"*Session"}, scoped: true,
			want: "servo: *example.com/httpcov.Session is scoped, but servo.Use wraps a singleton server that would capture it — middleware must be singletons"},
		{name: "function of another shape", uses: []string{"BadWrap"},
			want: "servo: example.com/httpcov.BadWrap must have a method Middleware(next http.Handler) http.Handler, or be a func(http.Handler) http.Handler, to be attached with servo.Use — it is a function, but not a func(http.Handler) http.Handler"},
		{name: "Middleware taking something else", uses: []string{"*ParamMiddleware"},
			want: "servo: *example.com/httpcov.ParamMiddleware must have a method Middleware(next http.Handler) http.Handler, or be a func(http.Handler) http.Handler, to be attached with servo.Use — its parameter list must be exactly (next http.Handler)"},
		{name: "Middleware returning something else", uses: []string{"*ResultMiddleware"},
			want: "servo: *example.com/httpcov.ResultMiddleware must have a method Middleware(next http.Handler) http.Handler, or be a func(http.Handler) http.Handler, to be attached with servo.Use — its result must be exactly http.Handler"},
		{name: "group this injector does not serve", uses: []string{"Wrap"}, groups: []string{"telemetry"}, useGroup: "nope",
			want: `servo: servo.Use[example.com/httpcov.Wrap] selects group "nope", but this injector serves only: default, telemetry`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := f.input(t)
			if tc.scoped {
				in.Spec.Scopes = []load.ScopeDecl{f.scope("Session", "Sessions")}
			}
			for _, e := range tc.extracts {
				in.HTTP.Extracts = append(in.HTTP.Extracts, f.extract(e))
			}
			for _, u := range tc.uses {
				d := f.use(u)
				if tc.useGroup != "" {
					d.Groups = []string{tc.useGroup}
				}
				in.HTTP.Uses = append(in.HTTP.Uses, d)
			}
			for _, g := range tc.groups {
				in.HTTP.Groups = append(in.HTTP.Groups, load.GroupDecl{Name: g, Pos: token.Position{Filename: "spec.go", Line: 13}})
			}
			if msg := onlyDiagnostic(t, in).Message; !strings.Contains(msg, tc.want) {
				t.Errorf("diagnostic missing %q:\n%s", tc.want, msg)
			}
		})
	}
}

// TestPlainMiddlewareFunctionIsAttachedAsItself: a func(http.Handler)
// http.Handler type needs no Middleware method — the chain calls the node
// itself — and with no selectors it wraps the whole server.
func TestPlainMiddlewareFunctionIsAttachedAsItself(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	in := f.input(t)
	in.HTTP.Uses = []load.UseDecl{f.use("Wrap")}
	r := mustResolve(t, in)
	if len(r.HTTP.Uses) != 1 || r.HTTP.Uses[0].Node.Key != graph.NewKey(f.typ("Wrap"), "") {
		t.Fatalf("uses = %+v, want the Wrap node", r.HTTP.Uses)
	}
	if got := HTTPUseScope(r.HTTP.Uses[0]); got != "server" {
		t.Errorf("HTTPUseScope = %q, want server", got)
	}
}

// TestHTTPUseScopeNamesWhatAnAttachmentWraps is the one phrase `servo
// graph` and the generated Graph() both print for a servo.Use: its route
// selectors, else its groups, else the whole server.
func TestHTTPUseScopeNamesWhatAnAttachmentWraps(t *testing.T) {
	for _, tc := range []struct {
		decl load.UseDecl
		want string
	}{
		{load.UseDecl{Routes: []load.RouteSel{{Pattern: "GET /a"}, {Pattern: "POST /b"}}}, "route GET /a, POST /b"},
		{load.UseDecl{Groups: []string{"admin", "telemetry"}}, "group admin, telemetry"},
		{load.UseDecl{}, "server"},
	} {
		if got := HTTPUseScope(&HTTPUse{Decl: tc.decl}); got != tc.want {
			t.Errorf("HTTPUseScope(%+v) = %q, want %q", tc.decl, got, tc.want)
		}
	}
}

// TestHTTPRouteArgsMarksExtractedParameters: a handler's arguments read
// in declaration order, a graph dependency by its key and an extracted
// one by the type its extractor produces, marked as such.
func TestHTTPRouteArgsMarksExtractedParameters(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	in := f.input(t, "httpcov.Me")
	in.HTTP.Extracts = []load.ExtractDecl{f.extract("*UserExtractor")}
	r := mustResolve(t, in)
	if len(r.HTTP.Routes) != 1 {
		t.Fatalf("routes = %+v", r.HTTP.Routes)
	}
	got := HTTPRouteArgs(r.HTTP.Routes[0])
	want := []string{"*example.com/httpcov.Repo", "*example.com/httpcov.User (extracted)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("HTTPRouteArgs = %q, want %q", got, want)
	}
}

const requestStructHint = "meant as the request struct"

// TestRequestStructHintOnlyWhereARequestStructCouldBe: the hint about
// binding tags is for a handler whose second parameter points to a struct
// and failed to resolve as a dependency. Anywhere else — a request struct
// already in place, a later parameter, an interface, a pointer to a basic
// or non-struct type — "no provider" is the whole story.
func TestRequestStructHintOnlyWhereARequestStructCouldBe(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	for _, tc := range []struct {
		handler, missing string
	}{
		{"httpcov.Order", "*example.com/httpcov.Missing"},
		{"httpcov.Second", "*example.com/httpcov.Missing"},
		{"httpcov.Iface", "example.com/httpcov.Store"},
		{"httpcov.Count", "*int"},
		{"httpcov.ModeOf", "*example.com/httpcov.Mode"},
	} {
		t.Run(tc.handler, func(t *testing.T) {
			msg := onlyDiagnostic(t, f.input(t, tc.handler)).Message
			if !strings.Contains(msg, "servo: no provider for "+tc.missing+"\n") {
				t.Errorf("diagnostic does not name %s:\n%s", tc.missing, msg)
			}
			if strings.Contains(msg, requestStructHint) {
				t.Errorf("diagnostic carries the request-struct hint:\n%s", msg)
			}
		})
	}
}

// TestRequestStructHintIsGivenOnce: a second handler failing on the same
// missing type adds no diagnostic of its own, so there is nothing new for
// the hint to extend — the first one already carries it.
func TestRequestStructHintIsGivenOnce(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	msg := onlyDiagnostic(t, f.input(t, "httpcov.LostA", "httpcov.LostB")).Message
	if n := strings.Count(msg, requestStructHint); n != 1 {
		t.Errorf("hint appears %d times, want once:\n%s", n, msg)
	}
}

// TestRequestStructTheGraphProvidesIsRefusedWithHowItIsProvided: the
// refusal names the way the graph provides the type, whichever it is — a
// scope, an extractor, or a //servo:config loading the struct the pointer
// points at.
func TestRequestStructTheGraphProvidesIsRefusedWithHowItIsProvided(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	settingsLine := lineContaining(t, httpCoverSrc, "type Settings struct")
	for _, tc := range []struct {
		handler string
		setup   func(in *Input)
		want    string
	}{
		{"httpcov.Join", func(in *Input) { in.Spec.Scopes = []load.ScopeDecl{f.scope("JoinReq", "JoinReqs")} },
			"servo: handler httpcov.Join (POST /join/{room}) takes *example.com/httpcov.JoinReq as its request struct, but the graph provides that type: a servo.Scoped declaration makes it a scoped instance\n"},
		{"httpcov.Authorize", func(in *Input) { in.HTTP.Extracts = []load.ExtractDecl{f.extract("*TokenExtractor")} },
			"servo: handler httpcov.Authorize (POST /token) takes *example.com/httpcov.Token as its request struct, but the graph provides that type: the extractor *example.com/httpcov.TokenExtractor produces it per request\n"},
		{"httpcov.Configure", func(in *Input) {
			typ := f.typ("Settings")
			in.Configs = []*graph.ConfigDecl{{
				Key: graph.NewKey(typ, ""), Type: typ, TypeName: "Settings", Prefix: "APP", Section: "app",
				PkgPath: "example.com/httpcov", PkgName: "httpcov", Pos: token.Position{Filename: "httpcov.go", Line: settingsLine},
			}}
		}, "servo: handler httpcov.Configure (POST /settings) takes *example.com/httpcov.Settings as its request struct, but the graph provides that type: it points at example.com/httpcov.Settings, which a //servo:config directive loads (httpcov.go:"},
	} {
		t.Run(tc.handler, func(t *testing.T) {
			in := f.input(t, tc.handler)
			tc.setup(&in)
			if msg := onlyDiagnostic(t, in).Message; !strings.Contains(msg, tc.want) {
				t.Errorf("diagnostic missing %q:\n%s", tc.want, msg)
			}
		})
	}
}

// TestScopedHandlerDependencyWithoutAnAccessor: a handler holding a type
// that is scoped only because it takes the key has no accessor of its own
// to switch to, so the advice names the scope's accessor in general
// rather than an interface that does not exist.
func TestScopedHandlerDependencyWithoutAnAccessor(t *testing.T) {
	f := checkHTTPCoverFixture(t)
	in := f.input(t, "httpcov.ReadLog")
	in.Spec.Scopes = []load.ScopeDecl{f.scope("Session", "Sessions")}

	msg := onlyDiagnostic(t, in).Message
	for _, want := range []string{
		"servo: *example.com/httpcov.SessionLog is scoped, but handler httpcov.ReadLog (GET /log) receives it from the HTTP server, a singleton that would capture it\n",
		"  Depend on the scope's accessor interface instead of on *example.com/httpcov.SessionLog directly,\n  and call Acquire(ctx) per request.\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Depend on the accessor instead") {
		t.Errorf("diagnostic names an accessor SessionLog does not have:\n%s", msg)
	}
}
