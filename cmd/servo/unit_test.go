package main

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
	"golang.org/x/tools/go/packages"
)

func key(t string) graph.Key { return graph.Key{Type: t} }

func node(k graph.Key, deps ...*resolve.Node) *resolve.Node {
	return &resolve.Node{Key: k, Deps: deps}
}

func TestFindNodeExactKeyMatch(t *testing.T) {
	logger := node(key("*example.com/app/logger.Logger"))
	resolved := &resolve.Resolved{Order: []*resolve.Node{logger}}

	got, err := findNode(resolved, "*example.com/app/logger.Logger")
	if err != nil {
		t.Fatalf("findNode: %v", err)
	}
	if got != logger {
		t.Errorf("got %v, want the logger node", got.Key)
	}
}

func TestFindNodeUniqueSuffixMatch(t *testing.T) {
	logger := node(key("*example.com/app/logger.Logger"))
	resolved := &resolve.Resolved{Order: []*resolve.Node{logger}}

	got, err := findNode(resolved, "logger.Logger")
	if err != nil {
		t.Fatalf("findNode: %v", err)
	}
	if got != logger {
		t.Errorf("got %v, want the logger node", got.Key)
	}
}

func TestFindNodeNoMatch(t *testing.T) {
	resolved := &resolve.Resolved{Order: []*resolve.Node{node(key("*example.com/app/logger.Logger"))}}

	_, err := findNode(resolved, "nope.Nothing")
	if err == nil || !strings.Contains(err.Error(), "no node matches") {
		t.Fatalf("got err=%v, want a 'no node matches' error", err)
	}
}

func TestFindNodeAmbiguousSuffixMatch(t *testing.T) {
	a := node(key("*example.com/app/foo.Thing"))
	b := node(key("*example.com/app/bar.Thing"))
	resolved := &resolve.Resolved{Order: []*resolve.Node{a, b}}

	_, err := findNode(resolved, "Thing")
	if err == nil || !strings.Contains(err.Error(), "matches multiple nodes") {
		t.Fatalf("got err=%v, want a 'matches multiple nodes' error", err)
	}
}

func TestJoinOrNone(t *testing.T) {
	if got := joinOrNone(nil); got != "none" {
		t.Errorf("joinOrNone(nil) = %q, want %q", got, "none")
	}
	if got := joinOrNone([]string{"a", "b"}); got != "a, b" {
		t.Errorf("joinOrNone = %q, want %q", got, "a, b")
	}
}

func TestMainModuleScope(t *testing.T) {
	loaded := &load.Loaded{All: []*packages.Package{
		{PkgPath: "example.com/app/main", Module: &packages.Module{Main: true}},
		{PkgPath: "golang.org/x/tools/go/packages", Module: &packages.Module{Main: false}},
		{PkgPath: "example.com/app/nomodule", Module: nil},
	}}
	scope := mainModuleScope(loaded)
	if !scope["example.com/app/main"] {
		t.Errorf("expected the main-module package to be in scope: %v", scope)
	}
	if scope["golang.org/x/tools/go/packages"] {
		t.Errorf("expected a non-main-module package to be excluded from scope: %v", scope)
	}
	if scope["example.com/app/nomodule"] {
		t.Errorf("expected a package with no Module info to be excluded from scope: %v", scope)
	}
}

// TestModuleRootIsThePrefixPositionsAreTrimmedAgainst covers both answers
// moduleRoot can give. The directory it returns is stripped from every
// position `servo graph` prints, which is the only reason those strings
// match the ones the generated App.Graph() carries. A package that came
// back without module information — a load that never resolved one — has
// no such prefix, and answering with anything other than the empty string
// would trim a path that was never there and mangle every position in the
// output.
func TestModuleRootIsThePrefixPositionsAreTrimmedAgainst(t *testing.T) {
	withModule := &load.Spec{InjectorPkg: &packages.Package{
		Module: &packages.Module{Dir: filepath.FromSlash("/src/app")},
	}}
	if got, want := moduleRoot(withModule), filepath.FromSlash("/src/app"); got != want {
		t.Errorf("moduleRoot with module info = %q, want %q", got, want)
	}

	withoutModule := &load.Spec{InjectorPkg: &packages.Package{}}
	if got := moduleRoot(withoutModule); got != "" {
		t.Errorf("moduleRoot without module info = %q, want the empty string so nothing is trimmed", got)
	}
}

func TestShortestPathFromRootReachable(t *testing.T) {
	leaf := node(key("leaf"))
	mid := node(key("mid"), leaf)
	root := node(key("root"), mid)
	resolved := &resolve.Resolved{Roots: []*resolve.Node{root}, Order: []*resolve.Node{leaf, mid, root}}

	path, ok := shortestPathFromRoot(resolved, leaf)
	if !ok {
		t.Fatal("expected leaf to be reachable from root")
	}
	if len(path) != 3 || path[0] != root || path[1] != mid || path[2] != leaf {
		t.Errorf("got path %v, want [root mid leaf]", path)
	}
}

func TestShortestPathFromRootUnreachable(t *testing.T) {
	root := node(key("root"))
	disconnected := node(key("disconnected")) // not reachable from any root's Deps chain

	resolved := &resolve.Resolved{Roots: []*resolve.Node{root}, Order: []*resolve.Node{root, disconnected}}

	if _, ok := shortestPathFromRoot(resolved, disconnected); ok {
		t.Error("expected a node with no path from any root to be unreachable")
	}
}

func TestUnifiedDiffAndDiffLines(t *testing.T) {
	out := unifiedDiff("a\nb\nc\n", "a\nx\nc\n", "file.go")
	if !strings.Contains(out, "-b") || !strings.Contains(out, "+x") {
		t.Errorf("expected a -b/+x diff, got:\n%s", out)
	}
	if !strings.Contains(out, "--- file.go (committed)") || !strings.Contains(out, "+++ file.go (fresh)") {
		t.Errorf("expected labeled headers, got:\n%s", out)
	}

	if out := unifiedDiff("same\n", "same\n", "file.go"); out != "--- file.go (committed)\n+++ file.go (fresh)\n" {
		t.Errorf("expected only the header for identical content, got:\n%s", out)
	}

	allAdded := diffLines(nil, []string{"a", "b"})
	if len(allAdded) != 2 || allAdded[0].kind != diffInsert || allAdded[1].kind != diffInsert {
		t.Errorf("got %v, want two inserts", allAdded)
	}

	allRemoved := diffLines([]string{"a", "b"}, nil)
	if len(allRemoved) != 2 || allRemoved[0].kind != diffDelete || allRemoved[1].kind != diffDelete {
		t.Errorf("got %v, want two deletes", allRemoved)
	}
}

func TestCalleeName(t *testing.T) {
	parseExpr := func(src string) ast.Expr {
		t.Helper()
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", src, err)
		}
		return e
	}

	cases := []struct {
		src  string // the call.Fun expression itself, unqualified/qualified/neither
		want string
	}{
		{"Register", "Register"},
		{"servo.Register", "Register"},
		{"1", ""},
	}
	for _, c := range cases {
		if got := calleeName(parseExpr(c.src)); got != c.want {
			t.Errorf("calleeName(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestExprString(t *testing.T) {
	parseExpr := func(src string) ast.Expr {
		t.Helper()
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("ParseExpr(%q): %v", src, err)
		}
		return e
	}

	cases := []struct {
		src  string
		want string
	}{
		{"Thing{}", "Thing"},
		{"&Thing{}", "Thing"},
		{"pkg.Thing{}", "pkg.Thing"},
		{"&pkg.Thing{}", "pkg.Thing"},
		{"Thing", "Thing"},
		{"pkg.Thing", "pkg.Thing"},
		{"42", "<expr>"},
	}
	for _, c := range cases {
		if got := exprString(parseExpr(c.src)); got != c.want {
			t.Errorf("exprString(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestFilterRejectedAndCandidates(t *testing.T) {
	scope := map[string]bool{"example.com/app/in": true}
	rejected := []graph.Rejected{
		{Pkg: "example.com/app/in", Name: "in.Foo"},
		{Pkg: "stdlib/out", Name: "out.Bar"},
	}
	if got := filterRejected(rejected, scope, true); len(got) != 2 {
		t.Errorf("showAll=true: got %d, want 2", len(got))
	}
	if got := filterRejected(rejected, scope, false); len(got) != 1 || got[0].Name != "in.Foo" {
		t.Errorf("showAll=false: got %v, want just in.Foo", got)
	}

	candidates := []*graph.Provider{
		{Pkg: "example.com/app/in", Name: "in.New"},
		{Pkg: "stdlib/out", Name: "out.New"},
	}
	if got := filterCandidates(candidates, scope, true); len(got) != 2 {
		t.Errorf("showAll=true: got %d, want 2", len(got))
	}
	if got := filterCandidates(candidates, scope, false); len(got) != 1 || got[0].Name != "in.New" {
		t.Errorf("showAll=false: got %v, want just in.New", got)
	}
}

func TestPrintJSON(t *testing.T) {
	if err := printJSON(map[string]int{"a": 1}); err != nil {
		t.Errorf("printJSON: %v", err)
	}
	if err := printJSON(make(chan int)); err == nil {
		t.Error("printJSON(chan) = nil error, want a marshal error (channels are not JSON-serializable)")
	}
}
