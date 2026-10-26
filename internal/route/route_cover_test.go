package route

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// TestBindKindStringNamesEverySource: the kind's name is what a binding
// diagnostic calls the source — "query-bound fields must be a scalar",
// "more than one binding tag (path, query)" — so each one needs its own,
// and a value outside the set must still print as something.
func TestBindKindStringNamesEverySource(t *testing.T) {
	for kind, want := range map[BindKind]string{
		BindPath:     "path",
		BindQuery:    "query",
		BindHeader:   "header",
		BindForm:     "form",
		BindBody:     "body",
		BindKind(42): "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("BindKind(%d).String() = %q, want %q", int(kind), got, want)
		}
	}
}

// TestDiagnosticErrorMatchesString: a scan diagnostic handed on as a plain
// error must read exactly as the scan prints it.
func TestDiagnosticErrorMatchesString(t *testing.T) {
	d := Diagnostic{Pos: token.Position{Filename: "app.go", Line: 3, Column: 1}, Message: "servo: duplicate route GET /a"}
	if d.Error() != d.String() || d.String() != "app.go:3:1: servo: duplicate route GET /a" {
		t.Errorf("Error() = %q, String() = %q, want both %q", d.Error(), d.String(), "app.go:3:1: servo: duplicate route GET /a")
	}
}

// TestHTTPConfigKeyIsThePointerToServosConfig: resolve and the pipeline
// both ask for the server's config node by this one key, so it has to be
// the pointer type the emitted server reads its settings from.
func TestHTTPConfigKeyIsThePointerToServosConfig(t *testing.T) {
	servoTypes := loadServoPackage(t).Types
	key, typ := HTTPConfigKey(servoTypes)
	if key.String() != "*"+graph.ServoPackagePath+".HTTPConfig" {
		t.Errorf("key = %s, want *%s.HTTPConfig", key, graph.ServoPackagePath)
	}
	if want := types.NewPointer(servoTypes.Scope().Lookup("HTTPConfig").Type()); !types.Identical(typ, want) {
		t.Errorf("type = %s, want %s", typ, want)
	}
}

// TestHTTPConfigKeyPanicsOnAServoWithoutHTTPConfig: a servo package with
// no HTTPConfig type is an internal version mismatch — the CLI and the
// library disagree about what servo.HTTP() needs — and the panic says so
// rather than dereferencing a nil object.
func TestHTTPConfigKeyPanicsOnAServoWithoutHTTPConfig(t *testing.T) {
	fset := token.NewFileSet()
	oldServo, err := new(types.Config).Check(graph.ServoPackagePath, fset, []*ast.File{mustParse(t, fset, "servo.go", "package servo\n")}, nil)
	if err != nil {
		t.Fatalf("typecheck fake servo: %v", err)
	}
	defer func() {
		if r := recover(); r != "servo package has no HTTPConfig type — internal version mismatch" {
			t.Errorf("recovered %v, want the version-mismatch panic", r)
		}
	}()
	HTTPConfigKey(oldServo)
}

// TestCheckDirectiveJudgesOneLine is the editor's view of a route
// directive: servovet hands it the tokens of one line and shows the
// problem, worded as the scan words it.
func TestCheckDirectiveJudgesOneLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		problem string
	}{
		{"get", []string{"/orders/{id}", "admin"}, ""},
		{"post", nil, "servo: //servo:post needs a route pattern, e.g. //servo:post /order/{category}/"},
		// Only the route verbs are route directives; a caller asking about
		// any other name is told which ones are.
		{"config", []string{"prefix=APP"}, "servo: //servo:config is not a route directive — the route verbs are " + graph.RouteVerbList},
	} {
		problem, ok := CheckDirective(tc.name, tc.args)
		if problem != tc.problem || ok != (tc.problem == "") {
			t.Errorf("CheckDirective(%q, %q) = (%q, %v), want (%q, %v)", tc.name, tc.args, problem, ok, tc.problem, tc.problem == "")
		}
	}
}

// TestScanJudgesOnlyTheMainModulesTypedPackages: a dependency's comments
// are not the reader's to fix, and a package loaded without type
// information has no handlers to validate a directive against, so the
// scan reports and routes nothing from either — malformed lines included.
func TestScanJudgesOnlyTheMainModulesTypedPackages(t *testing.T) {
	servo := loadServoPackage(t)
	const src = `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

//servo:get /ok
func OK(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }

//servo:get
func Broken(ctx context.Context) (servo.Json[*Resp], error) { return nil, nil }
`
	fset := token.NewFileSet()
	var pkgs []*packages.Package
	for i, mod := range []*packages.Module{nil, {Path: "example.org/dep"}, {Path: "example.com", Main: true}} {
		path := fmt.Sprintf("example.com/app%d", i)
		f := mustParse(t, fset, path+"/fixture.go", src)
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		pkg, err := (&types.Config{Importer: newPkgImporter(servo)}).Check(path, fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("typecheck: %v", err)
		}
		p := &packages.Package{Name: "app", PkgPath: path, Types: pkg, TypesInfo: info, Fset: fset, Syntax: []*ast.File{f}, Module: mod}
		if mod != nil && mod.Main {
			// In the main module, but with no type information to check
			// a handler against.
			p.TypesInfo = nil
		}
		pkgs = append(pkgs, p)
	}
	routes, diags := Scan(pkgs, servo.Types)
	if len(routes) != 0 || len(diags) != 0 {
		t.Fatalf("routes = %v, diags = %v, want neither", routes, diags)
	}
}

// TestSecondParameterIsARequestStructOnlyWhenItPointsToATaggedNamedStruct:
// a handler's second parameter is decoded from the request only when it
// points to a named struct with a binding or json tag. A value, a pointer
// to an unnamed struct and a pointer to a named non-struct are ordinary
// dependencies, tags or not; json tags alone are enough to make a body.
func TestSecondParameterIsARequestStructOnlyWhenItPointsToATaggedNamedStruct(t *testing.T) {
	routes, diags := scanOn(t, `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

type Mode string

type NoteReq struct {
	Note string `+"`json:\"note\"`"+`
}

//servo:get /plain
func Plain(ctx context.Context, name Mode) (servo.Json[*Resp], error) { return nil, nil }

//servo:get /anon
func Anon(ctx context.Context, q *struct{ ID string `+"`path:\"id\"`"+` }) (servo.Json[*Resp], error) {
	return nil, nil
}

//servo:get /mode
func ModeOf(ctx context.Context, m *Mode) (servo.Json[*Resp], error) { return nil, nil }

//servo:post /note
func Note(ctx context.Context, req *NoteReq) (servo.Json[*Resp], error) { return nil, nil }
`)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	byName := map[string]*Route{}
	for _, rt := range routes {
		byName[rt.Func.Name()] = rt
	}
	for _, name := range []string{"Plain", "Anon", "ModeOf"} {
		rt := byName[name]
		if rt == nil {
			t.Fatalf("no route for %s", name)
		}
		if rt.Req != nil || len(rt.Deps) != 1 {
			t.Errorf("%s: Req = %+v, Deps = %v, want no request struct and the parameter as a dependency", name, rt.Req, rt.Deps)
		}
	}
	note := byName["Note"]
	if note == nil || note.Req == nil {
		t.Fatalf("Note route = %+v, want *NoteReq as its request struct", note)
	}
	if f := note.Req.Fields; len(f) != 1 || f[0].Name != "Note" || f[0].Kind != BindBody || f[0].Param != "note" || !note.Req.HasBody {
		t.Errorf("Note request plan = %+v, want the one json body field", note.Req)
	}
}

// TestEndAnchorSegmentNeedsNoField: {$} anchors a pattern's end and binds
// nothing, so a request struct is not asked for a path field to match it.
func TestEndAnchorSegmentNeedsNoField(t *testing.T) {
	routes, diags := scanOn(t, `package app

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type Resp struct{}

type ItemsReq struct {
	Page int `+"`query:\"page\"`"+`
}

//servo:get /items/{$}
func Items(ctx context.Context, req *ItemsReq) (servo.Json[*Resp], error) { return nil, nil }
`)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(routes) != 1 || routes[0].Req == nil || len(routes[0].Req.Fields) != 1 || routes[0].Req.Fields[0].Kind != BindQuery {
		t.Fatalf("routes = %+v, want GET /items/{$} with its one query field", routes)
	}
}
