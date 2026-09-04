package render

import (
	"encoding/json"
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
	"github.com/okian/servo/v3/servo"
)

// suppliedFixtureSrc pairs the two things nothing else in this package
// reaches: a servo.Value, which is a node with no provider at all, and a
// scope whose key extractor takes a dependency of its own. Both end up in
// the same graph because both are answered by the same question — what
// does one instance of a scope borrow from the app, and what did the app
// never build in the first place.
const suppliedFixtureSrc = `
package app

import "context"

type TenantKey string

// Flags is what no constructor can produce — a command-line flag — so the
// spec declares it with servo.Value and the caller hands it to NewWith.
type Flags struct{ DSN string }

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

// Decoder is reached only through the key extractor's parameter list, so
// it is the one singleton that can only be found by walking
// ExtractorDeps.
type Decoder struct{}
func NewDecoder(l *Logger) *Decoder { return &Decoder{} }

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}

type Tenant struct{}
func NewTenant(k TenantKey, f Flags) *Tenant { return &Tenant{} }
func (_ *Tenant) ScopeKey(ctx context.Context, d *Decoder) (TenantKey, error) { return "", nil }

type Server struct{}
func NewServer(t Tenants, f Flags) *Server { return &Server{} }
`

// suppliedModRoot is the module directory the fixture's positions are
// reported under, so a test can ask for them relative and get a
// deterministic answer instead of whatever t.TempDir handed out.
const suppliedModRoot = "/mod/root"

// buildSuppliedResolved runs the real resolver, so the supplied node
// ToGraph converts is the one the pipeline builds — Kind, Level and the
// absent Provider included — rather than a struct literal that could
// disagree with it and never be noticed.
func buildSuppliedResolved(t *testing.T) *resolve.Resolved {
	t.Helper()
	ctxPkg := loadContextPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", suppliedFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: singlePkgImporter{ctxPkg.Types}}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	appPkg := &packages.Package{
		Name: "app", PkgPath: "example.com/app",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}
	candidates, _ := graph.ScanCandidates([]*packages.Package{appPkg}, "example.com/app")

	lookup := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }
	ptr := func(name string) types.Type { return types.NewPointer(lookup(name)) }

	resolved, diags := resolve.Resolve(resolve.Input{
		Spec: &load.Spec{
			Roots: []load.RootDecl{{Key: graph.NewKey(ptr("Server"), ""), Type: ptr("Server"), Pos: token.Position{Filename: suppliedModRoot + "/spec.go", Line: 5, Column: 3}}},
			Scopes: []load.ScopeDecl{{
				Impl: graph.NewKey(ptr("Tenant"), ""), ImplType: ptr("Tenant"),
				Iface: graph.NewKey(lookup("Tenants"), ""), IfaceType: lookup("Tenants"),
				Pos: token.Position{Filename: suppliedModRoot + "/spec.go", Line: 6, Column: 3},
			}},
			Values: []load.ValueDecl{{
				Key: graph.NewKey(lookup("Flags"), ""), Type: lookup("Flags"),
				Pos: token.Position{Filename: suppliedModRoot + "/spec.go", Line: 7, Column: 3},
			}},
		},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      map[string]bool{"example.com/app": true},
		Fset:       fset,
		Pkgs:       []*packages.Package{appPkg},
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved
}

// TestToGraphReportsASuppliedValueAsALevelZeroNode covers the one node
// kind that has no provider to read a binding or a position off. It is
// listed first and at level 0 because the app depends on it before it
// builds anything: a consumer walking levels in order must be able to
// treat "supplied" as already present, not as something to wait for.
func TestToGraphReportsASuppliedValueAsALevelZeroNode(t *testing.T) {
	g := ToGraph(buildSuppliedResolved(t), suppliedModRoot)

	if len(g.Nodes) == 0 {
		t.Fatal("no nodes")
	}
	flags := g.Nodes[0]
	if flags.Type != "example.com/app.Flags" {
		t.Fatalf("first node = %s, want the supplied value ahead of everything the app builds", flags.Type)
	}
	if flags.Binding != "supplied" {
		t.Errorf("Binding = %q, want supplied — there is no provider to have selected", flags.Binding)
	}
	if flags.Level != 0 {
		t.Errorf("Level = %d, want 0", flags.Level)
	}
	// nil, not []string{}: the generated App.Graph() writes nil for a node
	// with no dependencies, and the doc promises one schema.
	if flags.Deps != nil {
		t.Errorf("Deps = %v, want nil", flags.Deps)
	}
	// Read off SuppliedPos — the servo.Value call site — since Provider is
	// nil and dereferencing it for a position is what this branch exists
	// to avoid.
	if flags.Pos != "spec.go:7:3" {
		t.Errorf("Pos = %q, want the servo.Value call site relative to the module root", flags.Pos)
	}
	if flags.Scope != "" {
		t.Errorf("Scope = %q: a supplied value belongs to the app, not to any scope", flags.Scope)
	}
}

// TestScopeBorrowsASingletonReachedOnlyByTheKeyExtractor covers the
// ExtractorDeps half of borrowedOf. The extractor runs once per Acquire
// to turn a context into a key, so whatever it takes is constructed by
// the app and shared — exactly like a member's own dependency, and
// invisible if only members' dependencies are walked.
//
// The supplied value is the negative half: Tenant takes Flags, but the
// app never builds Flags, so reporting it as borrowed would name a
// construction that does not exist.
func TestScopeBorrowsASingletonReachedOnlyByTheKeyExtractor(t *testing.T) {
	g := ToGraph(buildSuppliedResolved(t), suppliedModRoot)

	if len(g.Scopes) != 1 {
		t.Fatalf("got %d scopes, want 1", len(g.Scopes))
	}
	borrows := strings.Join(g.Scopes[0].Borrows, ",")
	if !strings.Contains(borrows, "*example.com/app.Decoder") {
		t.Errorf("borrows = %v, want the extractor's own dependency listed", g.Scopes[0].Borrows)
	}
	if strings.Contains(borrows, "example.com/app.Flags") {
		t.Errorf("borrows = %v: a supplied value is handed in, not constructed by the app", g.Scopes[0].Borrows)
	}
}

// TestJSONCarriesTheSuppliedNode is the schema claim applied to the node
// kind that arrived last: `servo graph --format=json` and the generated
// App.Graph() are supposed to be the same document, so a supplied value
// has to survive the round trip with its binding and its relative
// position intact rather than being special-cased out of one of them.
func TestJSONCarriesTheSuppliedNode(t *testing.T) {
	out, err := JSON(ToGraph(buildSuppliedResolved(t), suppliedModRoot))
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var round servo.Graph
	if err := json.Unmarshal([]byte(out), &round); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, out)
	}
	if len(round.Nodes) == 0 || round.Nodes[0].Binding != "supplied" || round.Nodes[0].Pos != "spec.go:7:3" {
		t.Fatalf("supplied node did not round-trip: %+v", round.Nodes)
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Errorf("JSON output must end in a newline so it is a well-formed line-oriented file:\n%q", out)
	}
	if strings.Contains(out, suppliedModRoot) {
		t.Errorf("JSON output embeds the module's absolute directory:\n%s", out)
	}
}

// TestRelToMatchesTheGeneratedFilesPositions pins the whole point of
// relTo: `servo graph --format=json` has to print the strings the
// generated App.Graph() carries, and emit writes those relative to the
// module root so the committed file is byte-identical across checkouts at
// different absolute paths. Every case below is one where trimming would
// produce something the generated file never writes, so the position is
// left exactly as it came in instead.
func TestRelToMatchesTheGeneratedFilesPositions(t *testing.T) {
	cases := []struct {
		name    string
		modRoot string
		pos     string
		want    string
	}{
		{
			// A Resolved built by hand in a test has no module to be
			// relative to, and inventing one would rewrite positions
			// against a root that does not exist.
			name:    "no module root leaves the position alone",
			modRoot: "",
			pos:     "/mod/root/internal/store/store.go:12:6",
			want:    "/mod/root/internal/store/store.go:12:6",
		},
		{
			name:    "a position inside the module is trimmed to a module-relative path",
			modRoot: "/mod/root",
			pos:     "/mod/root/internal/store/store.go:12:6",
			want:    "internal/store/store.go:12:6",
		},
		{
			// A provider in a dependency module lives outside the tree
			// entirely. "../../go/pkg/mod/..." is not a path anyone can
			// open from the module root, so the absolute one is kept.
			name:    "a position outside the module stays absolute rather than becoming a ..-path",
			modRoot: "/mod/root",
			pos:     "/elsewhere/dep/dep.go:3:1",
			want:    "/elsewhere/dep/dep.go:3:1",
		},
		{
			// filepath.Rel cannot relate a relative path to an absolute
			// root, and the position is already in the form the generated
			// file writes.
			name:    "an already-relative position is passed through",
			modRoot: "/mod/root",
			pos:     "internal/store/store.go:12:6",
			want:    "internal/store/store.go:12:6",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RelTo(c.modRoot, c.pos); got != c.want {
				t.Errorf("RelTo(%q, %q) = %q, want %q", c.modRoot, c.pos, got, c.want)
			}
		})
	}
}
