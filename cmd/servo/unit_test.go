package main

import (
	"go/ast"
	"go/parser"
	"runtime"
	"slices"
	"strconv"
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

// TestFindNodeSuffixMatchesAtASegmentBoundary: a bare suffix test called
// `api.Server` ambiguous between api.Server and otherapi.Server, which
// made the short form useless in exactly the modules with enough packages
// to want it.
func TestFindNodeSuffixMatchesAtASegmentBoundary(t *testing.T) {
	api := node(key("*example.com/app/api.Server"))
	other := node(key("*example.com/app/otherapi.Server"))
	resolved := &resolve.Resolved{Order: []*resolve.Node{api, other}}

	got, err := findNode(resolved, "api.Server")
	if err != nil {
		t.Fatalf("findNode: %v", err)
	}
	if got != api {
		t.Errorf("got %v, want api.Server", got.Key)
	}
	// Without a boundary the query is still ambiguous, as it should be.
	if _, err := findNode(resolved, "Server"); err == nil || !strings.Contains(err.Error(), "matches multiple nodes") {
		t.Fatalf("got err=%v, want an ambiguity for a bare type name", err)
	}
	// Generic instantiations keep their arguments addressable.
	cache := node(key("*example.com/app/cache.Cache[example.com/app/order.Order]"))
	resolved = &resolve.Resolved{Order: []*resolve.Node{cache}}
	if got, err := findNode(resolved, "order.Order]"); err != nil || got != cache {
		t.Errorf("findNode(order.Order]) = %v, %v", got, err)
	}
}

// numberedLines is n distinct lines, "0" through n-1.
func numberedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = strconv.Itoa(i)
	}
	return lines
}

// allocatedBy reports the bytes fn allocates. The bounds it is checked
// against are orders of magnitude apart from what the defect cost, so the
// odd allocation from elsewhere in the process cannot decide a result.
func allocatedBy(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// replay rebuilds both sides from ops, which is what makes them a diff of
// a and b at all.
func replay(ops []diffOp) (a, b []string) {
	for _, op := range ops {
		if op.kind != diffInsert {
			a = append(a, op.line)
		}
		if op.kind != diffDelete {
			b = append(b, op.line)
		}
	}
	return a, b
}

// TestDiffLinesWorkIsBounded pins the cost, not just the output. The diff
// used to fill an (n+1)×(m+1) table whatever the inputs were — 3 GB and
// most of a minute for twenty thousand lines with one line changed, which
// is what this test itself used to cost — while claiming in the changelog
// that it trimmed the common ends first.
func TestDiffLinesWorkIsBounded(t *testing.T) {
	const n = 20000
	cases := []struct {
		name     string
		edit     func(b []string) []string
		maxBytes uint64
		edits    int
	}{
		{
			name:     "one line changed in the middle",
			edit:     func(b []string) []string { b[n/2] = "changed"; return b },
			maxBytes: 4 << 20,
			edits:    2,
		},
		{
			// Trimming cannot help here: the first and last lines both
			// differ, so the whole file is the middle the core has to diff.
			name: "a change at each end",
			edit: func(b []string) []string {
				b[0], b[n-1] = "first", "last"
				return b
			},
			maxBytes: 4 << 20,
			edits:    4,
		},
		{
			// Nothing in common: past maxEdits the core stops looking and
			// reports the middle replaced wholesale.
			name: "nothing in common",
			edit: func(b []string) []string {
				for i := range b {
					b[i] = "other " + b[i]
				}
				return b
			},
			maxBytes: 48 << 20,
			edits:    2 * n,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := numberedLines(n)
			b := c.edit(numberedLines(n))
			var ops []diffOp
			if got := allocatedBy(func() { ops = diffLines(a, b) }); got > c.maxBytes {
				t.Errorf("diffLines allocated %d bytes, want at most %d", got, c.maxBytes)
			}
			gotA, gotB := replay(ops)
			if !slices.Equal(gotA, a) || !slices.Equal(gotB, b) {
				t.Fatal("the ops do not rebuild both inputs")
			}
			edits := 0
			for _, op := range ops {
				if op.kind != diffEqual {
					edits++
				}
			}
			if edits != c.edits {
				t.Errorf("got %d edits, want %d", edits, c.edits)
			}
		})
	}
}

// TestDiffLinesIsMinimal checks the Myers core against the definition: a
// shortest edit script deletes and inserts exactly the lines outside a
// longest common subsequence. The inputs are small and drawn from a
// three-line alphabet, so they repeat lines the way generated code does
// (`}`, blank lines, `return nil`) and exercise every kind of tie.
func TestDiffLinesIsMinimal(t *testing.T) {
	alphabet := []string{"}", "", "return nil"}
	seed := uint32(1)
	next := func(n int) int { // xorshift: deterministic, no global state
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return int(seed % uint32(n))
	}
	lines := func() []string {
		out := make([]string, next(12))
		for i := range out {
			out[i] = alphabet[next(len(alphabet))]
		}
		return out
	}
	for range 2000 {
		a, b := lines(), lines()
		ops := diffLines(a, b)
		gotA, gotB := replay(ops)
		if !slices.Equal(gotA, a) || !slices.Equal(gotB, b) {
			t.Fatalf("diffLines(%q, %q): the ops do not rebuild both inputs", a, b)
		}
		edits := 0
		for _, op := range ops {
			if op.kind != diffEqual {
				edits++
			}
		}
		if want := len(a) + len(b) - 2*lcsLength(a, b); edits != want {
			t.Fatalf("diffLines(%q, %q) made %d edits, want %d", a, b, edits, want)
		}
	}
}

// lcsLength is the textbook quadratic longest-common-subsequence length,
// the reference the test above holds the real diff to.
func lcsLength(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

// TestUnifiedDiffShowsWhereAChangeIs is the stale report the review could
// not read: a merge that drops a stop call from one rollback path printed
// a bare `-	_ = a.stopIssuer(ctx)`, indistinguishable from the same call
// in every other path. A hunk header with line numbers, the declaration it
// sits in, and the lines around it place it.
func TestUnifiedDiffShowsWhereAChangeIs(t *testing.T) {
	committed := strings.Join([]string{
		"package main",
		"",
		"func (a *App) rollbackFromCache(ctx context.Context) {",
		"	_ = a.stopCache(ctx)",
		"	_ = a.stopStore(ctx)",
		"	_ = a.stopLogger(ctx)",
		"}",
		"",
		"func (a *App) rollbackFromIssuer(ctx context.Context) {",
		"	_ = a.stopCache(ctx)",
		"	_ = a.stopStore(ctx)",
		"	_ = a.stopLogger(ctx)",
		"}",
		"",
	}, "\n")
	fresh := strings.Replace(committed,
		"func (a *App) rollbackFromIssuer(ctx context.Context) {\n",
		"func (a *App) rollbackFromIssuer(ctx context.Context) {\n\t_ = a.stopIssuer(ctx)\n", 1)

	// The heading names the function the change is in, although that
	// function's own first line is inside the context: taken from above the
	// context, as git takes it, it would name rollbackFromCache instead.
	got := unifiedDiff(committed, fresh, "servo_gen.go")
	want := strings.Join([]string{
		"--- servo_gen.go (committed)",
		"+++ servo_gen.go (fresh)",
		"@@ -7,6 +7,7 @@ func (a *App) rollbackFromIssuer(ctx context.Context) {",
		" }",
		" ",
		" func (a *App) rollbackFromIssuer(ctx context.Context) {",
		"+\t_ = a.stopIssuer(ctx)",
		" \t_ = a.stopCache(ctx)",
		" \t_ = a.stopStore(ctx)",
		" \t_ = a.stopLogger(ctx)",
		"",
	}, "\n")
	if got != want {
		t.Errorf("unifiedDiff =\n%s\nwant\n%s", got, want)
	}
}

// TestUnifiedDiffSplitsDistantChanges: two changes far enough apart get a
// hunk each, and changes close together share one, as with `diff -u`.
func TestUnifiedDiffSplitsDistantChanges(t *testing.T) {
	a := numberedLines(40)
	b := numberedLines(40)
	b[5], b[30], b[33] = "five", "thirty", "thirty-three"
	got := unifiedDiff(strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n", "f.go")
	if n := strings.Count(got, "\n@@ "); n != 2 {
		t.Fatalf("got %d hunks, want 2 — the changes at 30 and 33 share one:\n%s", n, got)
	}
	for _, header := range []string{"@@ -3,7 +3,7 @@\n", "@@ -28,10 +28,10 @@\n"} {
		if !strings.Contains(got, header) {
			t.Errorf("missing hunk header %q in:\n%s", header, got)
		}
	}
}

// TestUnifiedDiffTruncates: a file that changed shape — another servo
// version's output, say — differs on every line, and the report has to
// stay readable enough to find the command it ends with.
func TestUnifiedDiffTruncates(t *testing.T) {
	a := numberedLines(2000)
	b := make([]string, len(a))
	for i := range b {
		b[i] = "other " + a[i]
	}
	got := unifiedDiff(strings.Join(a, "\n"), strings.Join(b, "\n"), "f.go")
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) > maxDiffLines+4 {
		t.Errorf("printed %d lines, want at most %d", len(lines), maxDiffLines+4)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "... 3600 more lines of diff not shown") {
		t.Errorf("last line = %q, want the count of what was left out", last)
	}
}

// TestUnifiedDiffShowsAMissingFinalNewline: a final newline is the one
// difference that is invisible as a line, so it must not be trimmed away
// when only one side has it, or the report would be a header and nothing
// else.
func TestUnifiedDiffShowsAMissingFinalNewline(t *testing.T) {
	got := unifiedDiff("a\nb", "a\nb\n", "f.go")
	if !strings.Contains(got, "@@ ") {
		t.Errorf("a missing final newline produced no hunk:\n%s", got)
	}
}

func TestHunkRange(t *testing.T) {
	for _, c := range []struct {
		start, count int
		want         string
	}{
		{0, 0, "0,0"}, // an insertion at the top of the file
		{4, 0, "4,0"}, // an empty side names the line it comes after
		{4, 1, "5"},
		{4, 3, "5,3"},
	} {
		if got := hunkRange(c.start, c.count); got != c.want {
			t.Errorf("hunkRange(%d, %d) = %q, want %q", c.start, c.count, got, c.want)
		}
	}
}
