package route

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// pkgImporter lets fixture sources share object identity (context.Context,
// servo.Json) with the real servo package loaded via go/packages — the same
// trick emit_test.go and capabilities_test.go use, for the same reason:
// without it, the scanner's identity checks silently never match.
type pkgImporter struct{ byPath map[string]*types.Package }

func newPkgImporter(roots ...*packages.Package) *pkgImporter {
	idx := &pkgImporter{byPath: map[string]*types.Package{}}
	var add func(p *packages.Package)
	add = func(p *packages.Package) {
		if _, ok := idx.byPath[p.PkgPath]; ok {
			return
		}
		idx.byPath[p.PkgPath] = p.Types
		for _, dep := range p.Imports {
			add(dep)
		}
	}
	for _, p := range roots {
		add(p)
	}
	return idx
}

func (i *pkgImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if pkg, ok := i.byPath[path]; ok {
		return pkg, nil
	}
	return nil, &importNotFoundError{path}
}

// add registers an in-memory-checked package so later fixtures can import it.
func (i *pkgImporter) add(path string, pkg *types.Package) { i.byPath[path] = pkg }

type importNotFoundError struct{ path string }

func (e *importNotFoundError) Error() string { return "pkgImporter: package not found: " + e.path }

func loadServoPackage(t *testing.T) *packages.Package {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports}
	pkgs, err := packages.Load(cfg, graph.ServoPackagePath)
	if err != nil {
		t.Fatalf("load servo package: %v", err)
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("expected exactly one loaded servo package, got %d", len(pkgs))
	}
	return pkgs[0]
}

type fixture struct {
	path string // import path, e.g. "example.com/app"
	src  string
}

// scanFixtures typechecks each fixture in order (so later ones can import
// earlier ones), wraps them as main-module packages, and runs Scan.
func scanFixtures(t *testing.T, fixtures ...fixture) ([]*Route, []Diagnostic) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	importer := newPkgImporter(servoPkg)

	fset := token.NewFileSet()
	mod := &packages.Module{Path: "example.com", Main: true}
	var pkgs []*packages.Package
	for _, fx := range fixtures {
		f, err := parser.ParseFile(fset, fx.path+"/fixture.go", fx.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", fx.path, err)
		}
		info := &types.Info{
			Defs: map[*ast.Ident]types.Object{},
			Uses: map[*ast.Ident]types.Object{},
		}
		conf := types.Config{Importer: importer}
		pkg, err := conf.Check(fx.path, fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("typecheck %s: %v", fx.path, err)
		}
		importer.add(fx.path, pkg)
		pkgs = append(pkgs, &packages.Package{
			Name:      pkg.Name(),
			PkgPath:   fx.path,
			Types:     pkg,
			TypesInfo: info,
			Fset:      fset,
			Syntax:    []*ast.File{f},
			Module:    mod,
		})
	}
	return Scan(pkgs, servoPkg.Types)
}

func scanOn(t *testing.T, src string) ([]*Route, []Diagnostic) {
	t.Helper()
	return scanFixtures(t, fixture{path: "example.com/app", src: src})
}

const repoSrc = `package repo

type Repository struct{ dsn string }
`

const orderSrc = `package app

import (
	"context"

	"example.com/repo"
	"github.com/okian/servo/v3/servo"
)

type OrderReq struct {
	Category string ` + "`path:\"category\"`" + `
	Page     int    ` + "`query:\"page\"`" + `
}

type OrderResp struct{ ID string }

//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, r *repo.Repository) (servo.Json[*OrderResp], error) {
	return servo.JSON(&OrderResp{}), nil
}
`

func TestScanOrderExample(t *testing.T) {
	routes, diags := scanFixtures(t,
		fixture{path: "example.com/repo", src: repoSrc},
		fixture{path: "example.com/app", src: orderSrc},
	)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
	rt := routes[0]
	if rt.Method != "POST" || rt.Pattern != "/order/{category}/" {
		t.Errorf("route = %s %s, want POST /order/{category}/", rt.Method, rt.Pattern)
	}
	if rt.Name != "app.Order" || rt.Pkg != "example.com/app" {
		t.Errorf("Name/Pkg = %s / %s", rt.Name, rt.Pkg)
	}
	if rt.Req == nil {
		t.Fatalf("Req plan is nil, want the tagged struct recognized")
	}
	if got := graph.TypeString(rt.Req.Type); got != "*example.com/app.OrderReq" {
		t.Errorf("Req.Type = %s", got)
	}
	if len(rt.Req.Fields) != 2 {
		t.Fatalf("got %d bound fields, want 2: %+v", len(rt.Req.Fields), rt.Req.Fields)
	}
	cat, page := rt.Req.Fields[0], rt.Req.Fields[1]
	if cat.Name != "Category" || cat.Kind != BindPath || cat.Param != "category" {
		t.Errorf("field 0 = %+v", cat)
	}
	if page.Name != "Page" || page.Kind != BindQuery || page.Param != "page" {
		t.Errorf("field 1 = %+v", page)
	}
	if rt.Req.HasBody {
		t.Errorf("HasBody = true for a struct with only path/query fields")
	}
	if got := graph.TypeString(rt.RespType); got != "*example.com/app.OrderResp" {
		t.Errorf("RespType = %s", got)
	}
	if len(rt.Deps) != 1 || rt.Deps[0].String() != "*example.com/repo.Repository" {
		t.Errorf("Deps = %v", rt.Deps)
	}
}

func TestScanAllMethods(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /a
func A(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /b
func B(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:put /c
func C(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:patch /d
func D(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:delete /e
func E(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:head /f
func F(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:options /g
func G(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	want := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	if len(routes) != len(want) {
		t.Fatalf("got %d routes, want %d", len(routes), len(want))
	}
	got := map[string]bool{}
	for _, rt := range routes {
		got[rt.Method] = true
	}
	for _, m := range want {
		if !got[m] {
			t.Errorf("method %s missing from %v", m, routes)
		}
	}
}

// Routes come back sorted by (Pattern, Method) so every downstream consumer
// — emit ordering, the generated header comment — is deterministic without
// re-sorting.
func TestScanSortsRoutes(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:post /zzz
func Z(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /aaa
func A(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /aaa
func B(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	var got []string
	for _, rt := range routes {
		got = append(got, rt.Method+" "+rt.Pattern)
	}
	want := []string{"GET /aaa", "POST /aaa", "POST /zzz"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestScanBindingKinds(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Req struct {
	ID      string  ` + "`path:\"id\"`" + `
	Page    uint16  ` + "`query:\"page\"`" + `
	Ratio   float64 ` + "`query:\"ratio\"`" + `
	Full    bool    ` + "`query:\"full\"`" + `
	Trace   string  ` + "`header:\"X-Trace-Id\"`" + `
	Note    string  ` + "`json:\"note\"`" + `
	Untagged string
}

type Resp struct{}

//servo:post /things/{id}
func Create(ctx context.Context, req *Req) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	rt := routes[0]
	if !rt.Req.HasBody {
		t.Fatalf("HasBody = false, want true (json + untagged fields)")
	}
	kinds := map[string]BindKind{}
	params := map[string]string{}
	for _, f := range rt.Req.Fields {
		kinds[f.Name] = f.Kind
		params[f.Name] = f.Param
	}
	expect := map[string]BindKind{
		"ID": BindPath, "Page": BindQuery, "Ratio": BindQuery, "Full": BindQuery,
		"Trace": BindHeader, "Note": BindBody, "Untagged": BindBody,
	}
	for name, kind := range expect {
		if kinds[name] != kind {
			t.Errorf("field %s kind = %v, want %v", name, kinds[name], kind)
		}
	}
	if params["Trace"] != "X-Trace-Id" {
		t.Errorf("Trace param = %q", params["Trace"])
	}
	if params["Note"] != "note" || params["Untagged"] != "Untagged" {
		t.Errorf("body params = %q, %q", params["Note"], params["Untagged"])
	}
}

func TestScanFormBinding(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Req struct {
	Name  string ` + "`form:\"name\"`" + `
	Count int    ` + "`form:\"count\"`" + `
}

type Resp struct{}

//servo:post /submit
func Submit(ctx context.Context, req *Req) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	rt := routes[0]
	if !rt.Req.HasForm || rt.Req.HasBody {
		t.Fatalf("HasForm/HasBody = %v/%v, want true/false", rt.Req.HasForm, rt.Req.HasBody)
	}
}

// A json:"-" field belongs to neither the body nor any bound source.
func TestScanSkipsJSONDashFields(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Req struct {
	ID       string ` + "`path:\"id\"`" + `
	Internal string ` + "`json:\"-\"`" + `
}

type Resp struct{}

//servo:get /things/{id}
func Get(ctx context.Context, req *Req) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	rt := routes[0]
	if len(rt.Req.Fields) != 1 || rt.Req.Fields[0].Name != "ID" {
		t.Fatalf("fields = %+v, want only ID", rt.Req.Fields)
	}
	if rt.Req.HasBody {
		t.Fatalf("HasBody = true; a json:\"-\" field is not a body field")
	}
}

// Multiple directives above one function register the same handler on
// several routes.
func TestScanMultipleDirectivesOneHandler(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /health
//servo:head /health
func Health(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2", len(routes))
	}
	if routes[0].Func != routes[1].Func {
		t.Fatalf("both routes must share the handler func")
	}
}

// A handler whose second parameter is an untagged struct pointer treats it
// as an ordinary dependency: the request-struct position is recognized by
// binding tags, not by position alone.
func TestScanUntaggedSecondParamIsDependency(t *testing.T) {
	routes, diags := scanFixtures(t,
		fixture{path: "example.com/repo", src: repoSrc},
		fixture{path: "example.com/app", src: `package app

import (
	"context"

	"example.com/repo"
	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /stats/{id}
func Stats(ctx context.Context, r *repo.Repository) (servo.Json[*Resp], error) { return nil, nil }
`},
	)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	rt := routes[0]
	if rt.Req != nil {
		t.Fatalf("Req = %+v, want nil (untagged struct is a dependency)", rt.Req)
	}
	if len(rt.Deps) != 1 || rt.Deps[0].String() != "*example.com/repo.Repository" {
		t.Fatalf("Deps = %v", rt.Deps)
	}
}

func TestScanTrailingWildcard(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Req struct {
	Path string ` + "`path:\"rest\"`" + `
}

type Resp struct{}

//servo:get /files/{rest...}
func Files(ctx context.Context, req *Req) (servo.Json[*Resp], error) { return nil, nil }
`
	_, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
}

// Functions without directives — exported, unexported, methods — are simply
// not routes; the scan stays silent about them.
func TestScanIgnoresUndirectedFunctions(t *testing.T) {
	src := `package app

import "context"

type Svc struct{}

func New(ctx context.Context) *Svc { return &Svc{} }

func (s *Svc) Helper() int { return 1 }

func internal() {}
`
	routes, diags := scanOn(t, src)
	if len(routes) != 0 || len(diags) != 0 {
		t.Fatalf("routes=%v diags=%v, want none", routes, diags)
	}
}

// Every malformed directive or handler shape is a positioned diagnostic —
// the //servo: prefix is reserved, so nothing under it may silently no-op.
func TestScanDiagnostics(t *testing.T) {
	const header = `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

var _ = servo.JSON[*Resp]
var _ = context.Background
`
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "unknown method",
			src: header + `
//servo:pots /order
func Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: `unknown //servo: directive "pots"`,
		},
		{
			name: "missing pattern",
			src: header + `
//servo:post
func Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "needs a route pattern",
		},
		{
			name: "pattern without leading slash",
			src: header + `
//servo:post order
func Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: `must start with "/"`,
		},
		{
			name: "trailing tokens after the group",
			src: header + `
//servo:post /order extra more
func Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "takes a pattern and an optional group",
		},
		{
			name: "directive not on a function",
			src: header + `
//servo:post /order
var X = 1

func Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must be the doc comment of a top-level function",
		},
		{
			name: "directive on a method",
			src: header + `
type Svc struct{}

//servo:post /order
func (s *Svc) Order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must be top-level functions, not methods",
		},
		{
			name: "directive on an unexported function",
			src: header + `
//servo:post /order
func order(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must be exported",
		},
		{
			name: "directive on a generic function",
			src: header + `
//servo:post /order
func Order[T any](ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must not be generic",
		},
		{
			name: "directive on a variadic function",
			src: header + `
//servo:post /order
func Order(ctx context.Context, extra ...string) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must not be variadic",
		},
		{
			name: "first parameter not context",
			src: header + `
//servo:post /order
func Order(name string) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "first parameter must be context.Context",
		},
		{
			name: "no parameters at all",
			src: header + `
//servo:post /order
func Order() (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "first parameter must be context.Context",
		},
		{
			name: "wrong result count",
			src: header + `
//servo:post /order
func Order(ctx context.Context) error { return nil }
`,
			want: "must return exactly (servo.Json[T], error)",
		},
		{
			name: "first result not servo.Json",
			src: header + `
//servo:post /order
func Order(ctx context.Context) (*Resp, error) { return nil, nil }
`,
			want: "must return exactly (servo.Json[T], error)",
		},
		{
			name: "second result a concrete error type",
			src: header + `
type MyErr struct{}

func (e *MyErr) Error() string { return "" }

//servo:post /order
func Order(ctx context.Context) (servo.Json[*Resp], *MyErr) { return nil, nil }
`,
			want: "implements error but is not the error interface itself",
		},
		{
			name: "non-scalar bound field",
			src: header + `
type ListReq struct {
	Tags []string ` + "`query:\"tags\"`" + `
}

//servo:get /list
func List(ctx context.Context, req *ListReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must be a scalar",
		},
		{
			name: "form and json body mixed",
			src: header + `
type MixReq struct {
	Name string ` + "`form:\"name\"`" + `
	Note string ` + "`json:\"note\"`" + `
}

//servo:post /mix
func Mix(ctx context.Context, req *MixReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "mixes form fields with JSON body fields",
		},
		{
			name: "body fields on a bodyless method",
			src: header + `
type GetReq struct {
	ID   string ` + "`path:\"id\"`" + `
	Note string ` + "`json:\"note\"`" + `
}

//servo:get /things/{id}
func Get(ctx context.Context, req *GetReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "carry no body",
		},
		{
			name: "path tag without a matching segment",
			src: header + `
type OrderReq struct {
	Category string ` + "`path:\"category\"`" + `
}

//servo:post /order
func Order(ctx context.Context, req *OrderReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "has no {category} segment",
		},
		{
			name: "segment without a matching path tag",
			src: header + `
type OrderReq struct {
	Page int ` + "`query:\"page\"`" + `
}

//servo:post /order/{category}
func Order(ctx context.Context, req *OrderReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: `no path:"category" field`,
		},
		{
			name: "duplicate binding within a source",
			src: header + `
type DupReq struct {
	Page int ` + "`query:\"page\"`" + `
	P    int ` + "`query:\"page\"`" + `
}

//servo:get /dup
func Dup(ctx context.Context, req *DupReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: `both bind query "page"`,
		},
		{
			name: "multiple binding tags on one field",
			src: header + `
type MultiReq struct {
	X string ` + "`path:\"x\" query:\"x\"`" + `
}

//servo:get /multi/{x}
func Multi(ctx context.Context, req *MultiReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "more than one binding tag",
		},
		{
			name: "unexported request struct",
			src: header + `
type orderReq struct {
	ID string ` + "`path:\"id\"`" + `
}

//servo:get /things/{id}
func Get(ctx context.Context, req *orderReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "must be exported",
		},
		{
			name: "binding tag on unexported field",
			src: header + `
type SecretReq struct {
	ID     string ` + "`path:\"id\"`" + `
	secret string ` + "`query:\"secret\"`" + `
}

//servo:get /things/{id}
func Get(ctx context.Context, req *SecretReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "unexported but carries a binding tag",
		},
		{
			name: "duplicate route",
			src: header + `
//servo:post /order
func OrderA(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order
func OrderB(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "duplicate route POST /order",
		},
		{
			name: "mux pattern conflict",
			src: header + `
type AReq struct {
	X string ` + "`path:\"x\"`" + `
}

type BReq struct {
	Y string ` + "`path:\"y\"`" + `
}

//servo:get /a/{x}
func A(ctx context.Context, req *AReq) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /a/{y}
func B(ctx context.Context, req *BReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "conflicts",
		},
		{
			name: "malformed mux pattern",
			src: header + `
type BadReq struct {
	X string ` + "`path:\"x\"`" + `
}

//servo:get /a/{x}suffix
func Bad(ctx context.Context, req *BadReq) (servo.Json[*Resp], error) { return nil, nil }
`,
			want: "cannot be registered",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, diags := scanOn(t, c.src)
			if len(diags) == 0 {
				t.Fatalf("no diagnostics, want one containing %q", c.want)
			}
			var msgs []string
			for _, d := range diags {
				msgs = append(msgs, d.String())
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, c.want) {
				t.Fatalf("diagnostics do not contain %q:\n%s", c.want, joined)
			}
			// Every diagnostic must carry a real position: it is the only
			// thing that makes a module-wide scan's error actionable.
			for _, d := range diags {
				if d.Pos.Line == 0 || d.Pos.Filename == "" {
					t.Errorf("diagnostic without a position: %q", d.Message)
				}
			}
		})
	}
}

// The duplicate-route diagnostic names both declarations — the second one
// alone would send the user hunting for the first.
func TestScanDuplicateRouteNamesBothPositions(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:post /order
func OrderA(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order
func OrderB(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	_, diags := scanOn(t, src)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "first declared at") {
		t.Fatalf("message %q does not name the first declaration", diags[0].Message)
	}
}

// The servo CLI can be newer than the module's servo/v3 library. Directives
// are just comments — they parse fine against a servo with no Json type —
// so that mismatch must surface as a diagnostic naming the fix, not a
// panic and not a silently dropped route.
func TestScanOldServoWithoutJsonType(t *testing.T) {
	fset := token.NewFileSet()
	oldServo, err := new(types.Config).Check("github.com/okian/servo/v3/servo", fset, []*ast.File{
		mustParse(t, fset, "servo.go", "package servo\n"),
	}, nil)
	if err != nil {
		t.Fatalf("typecheck fake servo: %v", err)
	}

	src := `package app

import "context"

type Resp struct{}

//servo:get /ping
func Ping(ctx context.Context) (*Resp, error) { return nil, nil }
`
	f := mustParse(t, fset, "example.com/app/fixture.go", src)
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: newPkgImporter(loadServoPackage(t))}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "app", PkgPath: "example.com/app",
		Types: pkg, TypesInfo: info, Fset: fset, Syntax: []*ast.File{f},
		Module: &packages.Module{Path: "example.com", Main: true},
	}}

	routes, diags := Scan(pkgs, oldServo)
	if len(routes) != 0 {
		t.Fatalf("routes = %v, want none", routes)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "update github.com/okian/servo/v3") {
		t.Fatalf("diags = %v, want one version-mismatch diagnostic", diags)
	}
}

func mustParse(t *testing.T, fset *token.FileSet, name, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func TestScanGroupToken(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /healthz telemetry
func Healthz(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /orders default
func Orders(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /plain
func Plain(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	byName := map[string]string{}
	for _, rt := range routes {
		byName[rt.Func.Name()] = rt.Group
	}
	// A literal "default" token normalizes to "", the default group's
	// internal name, so the two spellings cannot diverge downstream.
	if byName["Healthz"] != "telemetry" || byName["Orders"] != "" || byName["Plain"] != "" {
		t.Fatalf("groups = %v", byName)
	}
}

// The same method+pattern on two different groups is two different servers
// — legal. Duplicate detection and the ServeMux conflict probe are both
// per group.
func TestScanDuplicatesArePerGroup(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:post /order
func A(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order internal
func B(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /a/{x}
func C(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /a/{y} internal
func D(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	routes, diags := scanOn(t, src)
	if len(diags) != 0 || len(routes) != 4 {
		t.Fatalf("routes=%d diags=%v, want 4 routes and no diagnostics", len(routes), diags)
	}
}

func TestScanStillRejectsSameGroupConflicts(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:post /order internal
func A(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /order internal
func B(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	_, diags := scanOn(t, src)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "duplicate route POST /order") {
		t.Fatalf("diags = %v", diags)
	}
}

func TestScanRejectsBadGroupToken(t *testing.T) {
	src := `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /x bad group
func X(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /y sp@ce
func Y(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	_, diags := scanOn(t, src)
	if len(diags) != 2 {
		t.Fatalf("diags = %v, want 2", diags)
	}
	joined := diags[0].Message + "\n" + diags[1].Message
	if !strings.Contains(joined, `unexpected "group"`) || !strings.Contains(joined, "must match [A-Za-z0-9_-]+") {
		t.Fatalf("messages = %s", joined)
	}
}
