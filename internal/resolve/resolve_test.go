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

const appSrc = `
package app

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }
func NewLoggerDup() *Logger { return &Logger{} }

type DBIface interface{ Query() string }

type Postgres struct{}
func (p *Postgres) Query() string { return "" }
func NewPostgres(l *Logger) *Postgres { return &Postgres{} }

type Memory struct{}
func (m *Memory) Query() string { return "" }
func NewMemory() *Memory { return &Memory{} }

type Replica struct{}
func (r *Replica) Query() string { return "" }
func NewReplica() *Replica { return &Replica{} }

type Server struct{}
func NewServer(db DBIface, l *Logger) *Server { return &Server{} }

type RootTwo struct{}
func NewRootTwo(l *Logger) *RootTwo { return &RootTwo{} }

type Missing struct{}

type Server2 struct{}
func NewServer2(m *Missing) *Server2 { return &Server2{} }

type Server3 struct{}
func NewServer3(m *Missing) *Server3 { return &Server3{} }

type A struct{}
type B struct{}
func NewA(b *B) *A { return &A{} }
func NewB(a *A) *B { return &B{} }

type X struct{}
type Y struct{}
type Z struct{}
func NewX(y *Y) *X { return &X{} }
func NewY(z *Z) *Y { return &Y{} }
func NewZ(x *X) *Z { return &Z{} }
`

func checkFixture(t *testing.T) (*types.Package, []*graph.Provider) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", appSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	accepted, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/app")
	return pkg, accepted
}

func namedKey(pkg *types.Package, name string) graph.Key {
	return graph.NewKey(pkg.Scope().Lookup(name).Type(), "")
}

func ptrKey(pkg *types.Package, name string) graph.Key {
	return graph.NewKey(types.NewPointer(pkg.Scope().Lookup(name).Type()), "")
}

func namedType(pkg *types.Package, name string) types.Type { return pkg.Scope().Lookup(name).Type() }

func ptrType(pkg *types.Package, name string) types.Type {
	return types.NewPointer(pkg.Scope().Lookup(name).Type())
}

func findProvider(t *testing.T, providers []*graph.Provider, name string) *graph.Provider {
	t.Helper()
	for _, p := range providers {
		if p.Name == "app."+name {
			return p
		}
	}
	t.Fatalf("no provider named app.%s among %d providers", name, len(providers))
	return nil
}

func baseInput(pkg *types.Package, candidates []*graph.Provider, roots []load.RootDecl, binds []load.BindDecl) Input {
	// scope = every package path any candidate claims, so structural search
	// isn't accidentally scoped out unless a test narrows it deliberately.
	scope := map[string]bool{}
	for _, c := range candidates {
		scope[c.Pkg] = true
	}
	return Input{
		Spec:       &load.Spec{Roots: roots, Binds: binds},
		Candidates: candidates,
		Caps:       graph.EmptyCapabilities(),
		Scope:      scope,
	}
}

func TestResolveHappyPath(t *testing.T) {
	pkg, all := checkFixture(t)
	// Only include the providers relevant to this test's root so unrelated
	// fixture functions (Server2, A/B, duplicate Logger) don't interfere.
	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewPostgres"),
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	binds := []load.BindDecl{{Iface: namedKey(pkg, "DBIface"), IfaceType: namedType(pkg, "DBIface"), Concrete: ptrKey(pkg, "Postgres"), ConcreteType: ptrType(pkg, "Postgres")}}

	resolved, diags := Resolve(baseInput(pkg, candidates, roots, binds))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(resolved.Order) != 3 {
		t.Fatalf("got %d nodes in order, want 3 (Logger, Postgres, Server)", len(resolved.Order))
	}
	// DFS post-order: Server's deps (DBIface->Postgres, Logger) must be
	// fully resolved and appended before Server itself.
	last := resolved.Order[len(resolved.Order)-1]
	if last.Key != ptrKey(pkg, "Server") {
		t.Errorf("last constructed node = %s, want *app.Server", last.Key)
	}
	postgresNode := resolved.ByKey[ptrKey(pkg, "Postgres")]
	if postgresNode.Level != 2 {
		t.Errorf("Postgres level = %d, want 2 (1 + level(Logger)=1, since NewPostgres itself depends on Logger)", postgresNode.Level)
	}
	serverNode := resolved.ByKey[ptrKey(pkg, "Server")]
	if serverNode.Level != 3 {
		t.Errorf("Server level = %d, want 3 (1 + level(Postgres)=2)", serverNode.Level)
	}
	if postgresNode.Binding != "explicit bind" {
		t.Errorf("Postgres binding = %q, want %q", postgresNode.Binding, "explicit bind")
	}
	loggerNode := resolved.ByKey[ptrKey(pkg, "Logger")]
	if loggerNode.Binding != "sole candidate" {
		t.Errorf("Logger binding = %q, want %q", loggerNode.Binding, "sole candidate")
	}
	if loggerNode.Level != 1 {
		t.Errorf("Logger level = %d, want 1", loggerNode.Level)
	}
}

func TestResolveSoleImplementationAutoBind(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewMemory"), // the ONLY DBIface implementer in this candidate set
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	resolved, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	memNode := resolved.ByKey[ptrKey(pkg, "Memory")]
	if memNode == nil {
		t.Fatal("Memory node not resolved")
	}
	if memNode.Binding != "sole implementation" {
		t.Errorf("Memory binding = %q, want %q", memNode.Binding, "sole implementation")
	}
}

// TestResolveInterfaceAsRootAutoBinds covers servo.Root[T]() where T is
// itself an interface, not just an interface reached as a dependency:
// resolveKey/selectProvider are the same code path either way, but this is
// the only test declaring the interface as the root directly.
func TestResolveInterfaceAsRootAutoBinds(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewMemory"), // the ONLY DBIface implementer in this candidate set
	}
	roots := []load.RootDecl{{Key: namedKey(pkg, "DBIface"), Type: namedType(pkg, "DBIface"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	resolved, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(resolved.Roots) != 1 {
		t.Fatalf("got %d roots, want 1", len(resolved.Roots))
	}
	root := resolved.Roots[0]
	if root.Binding != "sole implementation" {
		t.Errorf("root binding = %q, want %q", root.Binding, "sole implementation")
	}
	if root.Key != ptrKey(pkg, "Memory") {
		t.Errorf("root resolved to %v, want the Memory provider's key", root.Key)
	}
}

// TestResolveInterfaceAsRootAmbiguity is TestResolveAmbiguousInterface's
// sibling with the interface declared as the root itself: the chain is
// empty (there is no consumer, the interface IS the root), so the
// diagnostic must still render sensibly with just a "root" line and no
// dangling "needed by" line.
func TestResolveInterfaceAsRootAmbiguity(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewMemory"),
		findProvider(t, all, "NewPostgres"), // two implementers, no Bind -> ambiguous
	}
	roots := []load.RootDecl{{Key: namedKey(pkg, "DBIface"), Type: namedType(pkg, "DBIface"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "no provider for example.com/app.DBIface") {
		t.Errorf("message = %q, want it to name DBIface as unresolved", msg)
	}
	if strings.Contains(msg, "needed by") {
		t.Errorf("message = %q, want no 'needed by' line: DBIface is the root itself, not a dependency", msg)
	}
	if !strings.Contains(msg, "root") {
		t.Errorf("message = %q, want a root line", msg)
	}
}

func TestResolveAmbiguousInterface(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewMemory"),
		findProvider(t, all, "NewPostgres"), // two implementers now, no Bind -> ambiguous
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "no provider for example.com/app.DBIface") {
		t.Errorf("message = %q, want it to name DBIface as unresolved", msg)
	}
	if !strings.Contains(msg, "types implement") {
		t.Errorf("message = %q, want the interface-ambiguity phrasing", msg)
	}
	if !strings.Contains(msg, "servo.Bind[example.com/app.DBIface, *example.com/app.Memory]()") {
		t.Errorf("message = %q, want a paste-ready Bind line for Memory", msg)
	}
	if !strings.Contains(msg, "servo.Bind[example.com/app.DBIface, *example.com/app.Postgres]()") {
		t.Errorf("message = %q, want a paste-ready Bind line for Postgres", msg)
	}
	if !strings.Contains(msg, "needed by *example.com/app.Server") {
		t.Errorf("message = %q, want the chain to name Server as the consumer", msg)
	}
	if !strings.Contains(msg, "root") {
		t.Errorf("message = %q, want a root line", msg)
	}
}

func TestResolveScopeExcludesOutOfScopeImplementation(t *testing.T) {
	pkg, all := checkFixture(t)
	memory := findProvider(t, all, "NewMemory")
	replica := *findProvider(t, all, "NewReplica")
	replica.Pkg = "example.com/outofscope" // simulate an implementer outside the scan scope

	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		memory,
		&replica,
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	in := baseInput(pkg, candidates, roots, nil)
	in.Scope = map[string]bool{"example.com/app": true} // deliberately excludes "example.com/outofscope"

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics (replica should be invisible, leaving Memory as sole implementer): %v", diags)
	}
	if resolved.ByKey[ptrKey(pkg, "Memory")] == nil {
		t.Fatal("expected Memory to be auto-bound as the sole in-scope implementation")
	}
	if resolved.ByKey[ptrKey(pkg, "Replica")] != nil {
		t.Error("Replica must not appear in the resolved graph at all")
	}
}

func TestResolveMissingProvider(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewServer2")}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server2"), Type: ptrType(pkg, "Server2"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "no provider for *example.com/app.Missing") {
		t.Errorf("message = %q, want it to name *app.Missing", msg)
	}
	if strings.Contains(msg, "implement") {
		t.Errorf("message = %q, a plain missing provider should not suggest Bind (no candidates exist)", msg)
	}
}

func TestResolveAmbiguousExactDuplicate(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewLoggerDup"),
		findProvider(t, all, "NewMemory"),
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "2 functions produce *example.com/app.Logger") {
		t.Errorf("message = %q, want the duplicate-producer phrasing", msg)
	}
	if strings.Contains(msg, "servo.Bind[") {
		t.Errorf("message = %q, a same-type duplicate has no Bind fix to suggest", msg)
	}
}

func TestResolveCycle(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewA"), findProvider(t, all, "NewB")}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "A"), Type: ptrType(pkg, "A"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "dependency cycle") {
		t.Errorf("message = %q, want a cycle diagnostic", msg)
	}
	if !strings.Contains(msg, "*example.com/app.A") || !strings.Contains(msg, "*example.com/app.B") {
		t.Errorf("message = %q, want both A and B named in the loop", msg)
	}
}

// TestResolveCycleLongerThanTwoNodes covers cycleDiagnostic's loop-slicing
// logic at length 3 (X -> Y -> Z -> X), not just the 2-node case
// TestResolveCycle exercises — the loop must name every node in the cycle,
// not just the two nodes nearest the re-entry point.
func TestResolveCycleLongerThanTwoNodes(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewX"), findProvider(t, all, "NewY"), findProvider(t, all, "NewZ")}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "X"), Type: ptrType(pkg, "X"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "dependency cycle") {
		t.Errorf("message = %q, want a cycle diagnostic", msg)
	}
	for _, name := range []string{"*example.com/app.X", "*example.com/app.Y", "*example.com/app.Z"} {
		if !strings.Contains(msg, name) {
			t.Errorf("message = %q, want %s named in the loop", msg, name)
		}
	}
	if !strings.Contains(msg, "cycle closes here") {
		t.Errorf("message = %q, want the closing-line marker", msg)
	}
	// X must appear exactly twice: once opening the loop, once closing it.
	if got := strings.Count(msg, "*example.com/app.X"); got != 2 {
		t.Errorf("X appears %d times in the message, want exactly 2 (opens and closes the loop)", got)
	}
}

// TestResolveSharedDependencyAcrossTwoRootsIsASingleton covers a node
// reachable from two different declared roots (Postgres and RootTwo both
// depend directly on Logger): the shared dependency must be constructed
// exactly once and the same *Node instance shared by both consumers, not
// two separately-resolved nodes that merely compare equal by key.
func TestResolveSharedDependencyAcrossTwoRootsIsASingleton(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewPostgres"),
		findProvider(t, all, "NewRootTwo"),
	}
	roots := []load.RootDecl{
		{Key: ptrKey(pkg, "Postgres"), Type: ptrType(pkg, "Postgres"), Pos: token.Position{Filename: "spec.go", Line: 9}},
		{Key: ptrKey(pkg, "RootTwo"), Type: ptrType(pkg, "RootTwo"), Pos: token.Position{Filename: "spec.go", Line: 10}},
	}

	resolved, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(resolved.Roots) != 2 {
		t.Fatalf("got %d roots, want 2", len(resolved.Roots))
	}

	loggerKey := ptrKey(pkg, "Logger")
	loggerCount := 0
	for _, n := range resolved.Order {
		if n.Key == loggerKey {
			loggerCount++
		}
	}
	if loggerCount != 1 {
		t.Fatalf("Logger appears %d times in resolved.Order, want exactly 1 (constructed once)", loggerCount)
	}

	sharedLogger := resolved.ByKey[loggerKey]
	if sharedLogger == nil {
		t.Fatal("Logger missing from resolved.ByKey")
	}
	for _, root := range resolved.Roots {
		if len(root.Deps) != 1 {
			t.Fatalf("root %s has %d deps, want 1", root.Key, len(root.Deps))
		}
		if root.Deps[0] != sharedLogger {
			t.Errorf("root %s's Logger dependency is a different *Node instance than resolved.ByKey's, not a shared singleton", root.Key)
		}
	}
}

func TestResolveOverridesWinOverBinds(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{
		findProvider(t, all, "NewServer"),
		findProvider(t, all, "NewLogger"),
		findProvider(t, all, "NewMemory"),
		findProvider(t, all, "NewPostgres"),
	}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	binds := []load.BindDecl{{Iface: namedKey(pkg, "DBIface"), Concrete: ptrKey(pkg, "Postgres")}}
	extra := []load.BindDecl{{Iface: namedKey(pkg, "DBIface"), Concrete: ptrKey(pkg, "Memory")}}

	in := baseInput(pkg, candidates, roots, binds)
	in.ExtraBinds = extra
	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if resolved.ByKey[ptrKey(pkg, "Memory")] == nil {
		t.Error("expected Memory (from ExtraBinds/Override) to win over the normal Postgres Bind")
	}
	if resolved.ByKey[ptrKey(pkg, "Postgres")] != nil {
		t.Error("Postgres should not be constructed once overridden away")
	}
}

// TestDiagnosticErrorMatchesString covers the Error() method directly:
// Diagnostic implements error purely so it can be handed to callers that
// want a plain `error`, and Error() must render identically to String()
// rather than drifting into its own format over time.
func TestDiagnosticErrorMatchesString(t *testing.T) {
	d := Diagnostic{Pos: token.Position{Filename: "spec.go", Line: 9}, Message: "servo: no provider for example.com/app.Missing"}
	if d.Error() != d.String() {
		t.Errorf("Error() = %q, String() = %q, want identical", d.Error(), d.String())
	}
}

// TestResolveSecondConsumerOfSameMissingKeyReportsOneDiagnostic covers
// resolveKey's failedKey short-circuit: Server2 and Server3 both depend
// directly on the same unresolvable *Missing. Without the memoization at
// the top of resolveKey, the second consumer would re-run selectProvider
// and append a second, identical "no provider for *Missing" diagnostic.
func TestResolveSecondConsumerOfSameMissingKeyReportsOneDiagnostic(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewServer2"), findProvider(t, all, "NewServer3")}
	roots := []load.RootDecl{
		{Key: ptrKey(pkg, "Server2"), Type: ptrType(pkg, "Server2"), Pos: token.Position{Filename: "spec.go", Line: 9}},
		{Key: ptrKey(pkg, "Server3"), Type: ptrType(pkg, "Server3"), Pos: token.Position{Filename: "spec.go", Line: 10}},
	}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want exactly 1 (both consumers share the same failed *Missing key): %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].String(), "no provider for *example.com/app.Missing") {
		t.Errorf("message = %q, want it to name *app.Missing", diags[0].String())
	}
}

// TestResolveSameConcreteReachedViaInterfaceRootAndDirectRootIsShared
// covers resolveKey's colorBlack reuse branch: DBIface (bound to Postgres)
// and *Postgres itself are declared as two separate roots, so Postgres is
// reached via two different keys. The second arrival must reuse the
// already-fully-resolved node instead of constructing Postgres (and its
// Logger dependency) a second time.
func TestResolveSameConcreteReachedViaInterfaceRootAndDirectRootIsShared(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewPostgres"), findProvider(t, all, "NewLogger")}
	roots := []load.RootDecl{
		{Key: namedKey(pkg, "DBIface"), Type: namedType(pkg, "DBIface"), Pos: token.Position{Filename: "spec.go", Line: 9}},
		{Key: ptrKey(pkg, "Postgres"), Type: ptrType(pkg, "Postgres"), Pos: token.Position{Filename: "spec.go", Line: 10}},
	}
	binds := []load.BindDecl{{Iface: namedKey(pkg, "DBIface"), Concrete: ptrKey(pkg, "Postgres")}}

	resolved, diags := Resolve(baseInput(pkg, candidates, roots, binds))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if len(resolved.Order) != 2 {
		t.Fatalf("got %d nodes in resolved.Order, want 2 (Logger, Postgres constructed once each): %v", len(resolved.Order), resolved.Order)
	}
	if resolved.Roots[0].Key != ptrKey(pkg, "Postgres") || resolved.Roots[1].Key != ptrKey(pkg, "Postgres") {
		t.Errorf("both roots should resolve to the same Postgres node key, got %s and %s", resolved.Roots[0].Key, resolved.Roots[1].Key)
	}
	if resolved.Roots[0] != resolved.Roots[1] {
		t.Error("the interface root and the direct root should share the identical *Node instance, not two equal-by-key copies")
	}
}

// TestResolveExplicitBindToProviderlessConcreteType covers selectProvider's
// explicitUsed branch with zero exact candidates: a Bind naming a concrete
// type nobody actually provides. This must report the bind target itself
// as the unresolved key with plain "no provider" phrasing (isInterface
// false, no candidate list) — not fall through to structural search, which
// is only for keys reached without an explicit Bind.
func TestResolveExplicitBindToProviderlessConcreteType(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewServer"), findProvider(t, all, "NewLogger")}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	// Missing has no provider in the fixture at all — a Bind naming it is
	// exactly as dangling as if the user forgot to write New for it.
	binds := []load.BindDecl{{Iface: namedKey(pkg, "DBIface"), Concrete: ptrKey(pkg, "Missing")}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, binds))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "no provider for *example.com/app.Missing") {
		t.Errorf("message = %q, want it to name the dangling Bind target *app.Missing", msg)
	}
	if strings.Contains(msg, "implement") {
		t.Errorf("message = %q, a dangling Bind to a concrete type should not suggest implementers", msg)
	}
}

// TestResolveInterfaceWithNoImplementationsAtAll covers structural search's
// zero-match case: DBIface is required with no Bind and no candidate
// implements it at all (as opposed to TestResolveAmbiguousInterface's two
// implementers, or the auto-bind tests' exactly one). The diagnostic must
// still name DBIface with no candidate list, since there is nothing to
// suggest.
func TestResolveInterfaceWithNoImplementationsAtAll(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewServer"), findProvider(t, all, "NewLogger")}
	roots := []load.RootDecl{{Key: ptrKey(pkg, "Server"), Type: ptrType(pkg, "Server"), Pos: token.Position{Filename: "spec.go", Line: 9}}}

	_, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	msg := diags[0].String()
	if !strings.Contains(msg, "no provider for example.com/app.DBIface") {
		t.Errorf("message = %q, want it to name DBIface as unresolved", msg)
	}
	if strings.Contains(msg, "implement") {
		t.Errorf("message = %q, want no candidate list when nothing implements DBIface", msg)
	}
}
