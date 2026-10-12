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
)

const traceAppSrc = `
package app

import "context"

type Logger struct{}
func NewLogger() *Logger { return &Logger{} }

type Store interface {
	Get(ctx context.Context, key string) (string, error)
	Close() error
}

type Postgres struct{}
func (p *Postgres) Get(ctx context.Context, key string) (string, error) { return "", nil }
func (p *Postgres) Close() error { return nil }
func NewPostgres(l *Logger) *Postgres { return &Postgres{} }

type Memory struct{}
func (m *Memory) Get(ctx context.Context, key string) (string, error) { return "", nil }
func (m *Memory) Close() error { return nil }
func NewMemory() *Memory { return &Memory{} }

// TracerProvider stands in for go.opentelemetry.io/otel/trace.TracerProvider:
// the resolver only needs a key and a type to depend on.
type TracerProvider interface{ Tracer(name string) string }
type SDK struct{}
func (s *SDK) Tracer(name string) string { return name }
func NewSDK(l *Logger) *SDK { return &SDK{} }

// LoopSDK is a tracer provider that itself takes the traced interface —
// a cycle through the wrapper.
type LoopSDK struct{}
func (s *LoopSDK) Tracer(name string) string { return name }
func NewLoopSDK(s Store) *LoopSDK { return &LoopSDK{} }

type Server struct{}
func NewServer(s Store, l *Logger) *Server { return &Server{} }

type Worker struct{}
func NewWorker(s Store) *Worker { return &Worker{} }

// Direct takes the concrete type, and so bypasses the wrapper.
type Direct struct{}
func NewDirect(p *Postgres) *Direct { return &Direct{} }

// Pinger is a second interface Postgres implements, so one implementation
// can sit behind two wrappers.
type Pinger interface{ Ping(ctx context.Context) error }
func (p *Postgres) Ping(ctx context.Context) error { return nil }
type Prober struct{}
func NewProber(p Pinger) *Prober { return &Prober{} }

// NewTracerProvider returns the interface itself: an exact-type candidate
// inside the main module, which outranks the structural search.
func NewTracerProvider(l *Logger) TracerProvider { return &SDK{} }
`

func checkTraceFixture(t *testing.T) (*types.Package, []*graph.Provider) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", traceAppSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importerFor(loadPkg(t, "context"))}
	pkg, err := conf.Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgsPkg := &packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg, Fset: fset}
	accepted, _ := graph.ScanCandidates([]*packages.Package{pkgsPkg}, "example.com/app")
	return pkg, accepted
}

func traceDecl(pkg *types.Package, iface string) load.TraceDecl {
	return load.TraceDecl{Iface: namedKey(pkg, iface), IfaceType: namedType(pkg, iface), Pos: token.Position{Filename: "spec.go", Line: 12}}
}

func traceInput(pkg *types.Package) *TraceInput {
	return &TraceInput{ProviderKey: namedKey(pkg, "TracerProvider"), ProviderType: namedType(pkg, "TracerProvider")}
}

// providers picks the named constructors out of the fixture's candidate
// index, so a test's graph contains exactly what it names.
func providers(t *testing.T, all []*graph.Provider, names ...string) []*graph.Provider {
	t.Helper()
	out := make([]*graph.Provider, len(names))
	for i, n := range names {
		out[i] = findProvider(t, all, n)
	}
	return out
}

func storeBind(pkg *types.Package, concrete string) load.BindDecl {
	return load.BindDecl{Iface: namedKey(pkg, "Store"), IfaceType: namedType(pkg, "Store"), Concrete: ptrKey(pkg, concrete), ConcreteType: ptrType(pkg, concrete)}
}

func findTraced(t *testing.T, resolved *Resolved, iface graph.Key) *Node {
	t.Helper()
	n, ok := resolved.ByKey[tracedKey(iface)]
	if !ok {
		t.Fatalf("no wrapper node for %s", iface)
	}
	return n
}

// TestTraceWrapsEveryConsumer is the core contract: one wrapper per traced
// interface, handed to every consumer, built from the implementation and
// the tracer provider, with no capabilities of its own.
func TestTraceWrapsEveryConsumer(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewServer", "NewWorker"),
		[]load.RootDecl{rootDecl(pkg, "Server"), rootDecl(pkg, "Worker")},
		[]load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Traced) != 1 {
		t.Fatalf("Resolved.Traced has %d nodes, want 1", len(resolved.Traced))
	}
	w := resolved.Traced[0]
	if w.Kind != NodeTraced || w.Binding != "traced" || w.Key.String() != "example.com/app.Store#traced" {
		t.Fatalf("wrapper = kind %d, binding %q, key %s", w.Kind, w.Binding, w.Key)
	}
	if len(w.Capabilities) != 0 {
		t.Fatalf("wrapper capabilities = %v, want none: the implementation keeps its own lifecycle", w.Capabilities)
	}
	postgres, sdk := resolved.ByKey[ptrKey(pkg, "Postgres")], resolved.ByKey[ptrKey(pkg, "SDK")]
	if w.Traced.Inner != postgres || w.Traced.Tracer != sdk || len(w.Deps) != 2 || w.Deps[0] != postgres || w.Deps[1] != sdk {
		t.Fatalf("wrapper deps = %v, want [*Postgres, *SDK]", w.Deps)
	}
	if w.Traced.Inner.Binding != "explicit bind" {
		t.Fatalf("inner binding = %q: the interface must resolve exactly as it would untraced", w.Traced.Inner.Binding)
	}
	if w.Level != 3 {
		t.Fatalf("wrapper level = %d, want 3 (one above Postgres and SDK, both at 2)", w.Level)
	}
	server, worker := resolved.ByKey[ptrKey(pkg, "Server")], resolved.ByKey[ptrKey(pkg, "Worker")]
	if server.Deps[0] != w || worker.Deps[0] != w {
		t.Fatalf("consumers must share the one wrapper: server dep %s, worker dep %s", server.Deps[0].Key, worker.Deps[0].Key)
	}
	if server.Level != 4 {
		t.Fatalf("server level = %d, want 4", server.Level)
	}

	var order []string
	for _, n := range resolved.Order {
		order = append(order, n.Key.String())
	}
	got := strings.Join(order, ",")
	want := "*example.com/app.Logger,*example.com/app.Postgres,*example.com/app.SDK,example.com/app.Store#traced,*example.com/app.Server,*example.com/app.Worker"
	if got != want {
		t.Fatalf("construction order:\n got %s\nwant %s", got, want)
	}
}

// TestTraceConcreteConsumerBypassesWrapper: tracing is an interface-edge
// decision, so a consumer holding the concrete type still gets the
// implementation itself — the same node the wrapper is built around.
func TestTraceConcreteConsumerBypassesWrapper(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewServer", "NewDirect"),
		[]load.RootDecl{rootDecl(pkg, "Server"), rootDecl(pkg, "Direct")},
		[]load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	direct := resolved.ByKey[ptrKey(pkg, "Direct")]
	w := findTraced(t, resolved, namedKey(pkg, "Store"))
	if direct.Deps[0] != w.Traced.Inner || direct.Deps[0].Kind != NodeProvider {
		t.Fatalf("Direct got %s, want the untraced *Postgres the wrapper also holds", direct.Deps[0].Key)
	}
}

func TestTraceUnusedIsRejected(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewDirect"),
		[]load.RootDecl{rootDecl(pkg, "Direct")}, nil)
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic", resolved != nil, len(diags))
	}
	msg := diags[0].String()
	for _, want := range []string{
		"spec.go:12",
		"servo.Trace[example.com/app.Store]() is declared, but nothing in the graph depends on example.com/app.Store",
		"delete the servo.Trace declaration",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestTraceWithoutOtelInBuildIsRejected(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	// in.Trace deliberately nil: the trace API is not in the build.

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic", resolved != nil, len(diags))
	}
	msg := diags[0].String()
	for _, want := range []string{
		"servo.Trace[example.com/app.Store] needs go.opentelemetry.io/otel/trace, which nothing in this module's build imports",
		"func NewTracerProvider() (*sdktrace.TracerProvider, error)",
		"go get go.opentelemetry.io/otel/sdk",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestTraceMissingTracerProviderNamesTheFix: the tracer provider is the
// one dependency nobody wrote a parameter for, so its no-provider message
// has to say whose job writing one is, and point at the Trace declaration
// through the chain.
func TestTraceMissingTracerProviderNamesTheFix(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	msg := diags[0].String()
	for _, want := range []string{
		"spec.go:12: servo: no provider for example.com/app.TracerProvider",
		"needed by the servo.Trace wrapper for example.com/app.Store  spec.go:12",
		"needed by *example.com/app.Server",
		"there is no global fallback",
		"type Tracer struct{ *sdktrace.TracerProvider }",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

// TestTraceProviderIgnoresForeignExactCandidates: the OpenTelemetry
// packages themselves return trace.TracerProvider from several functions
// (the global getter, the no-op provider), and the exact-type step sees
// every module's candidates. For this one key those are never the answer,
// so the user's own implementation has to win over them.
func TestTraceProviderIgnoresForeignExactCandidates(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)
	// Two out-of-scope functions returning the provider interface exactly,
	// as otel.GetTracerProvider and trace.NewNoopTracerProvider do.
	tp := namedType(pkg, "TracerProvider")
	for _, name := range []string{"otel.GetTracerProvider", "trace.NewNoopTracerProvider"} {
		in.Candidates = append(in.Candidates, &graph.Provider{
			Result: namedKey(pkg, "TracerProvider"), ResultType: tp,
			Pkg: "go.opentelemetry.io/otel/elsewhere", Name: name,
			Pos: token.Position{Filename: "elsewhere.go", Line: 1},
		})
	}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	w := findTraced(t, resolved, namedKey(pkg, "Store"))
	if w.Traced.Tracer.Key != ptrKey(pkg, "SDK") || w.Traced.Tracer.Binding != "sole implementation" {
		t.Fatalf("tracer resolved to %s (%s), want the module's own *SDK by structural search", w.Traced.Tracer.Key, w.Traced.Tracer.Binding)
	}
}

// TestTraceProviderExactCandidateInScopeWins: an in-module function
// returning the interface itself is an ordinary exact match, and keeps
// winning over the structural search even with foreign exact candidates
// in the index.
func TestTraceProviderExactCandidateInScopeWins(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewTracerProvider", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)
	in.Candidates = append(in.Candidates, &graph.Provider{
		Result: namedKey(pkg, "TracerProvider"), ResultType: namedType(pkg, "TracerProvider"),
		Pkg: "go.opentelemetry.io/otel", Name: "otel.GetTracerProvider",
		Pos: token.Position{Filename: "elsewhere.go", Line: 1},
	})

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	w := findTraced(t, resolved, namedKey(pkg, "Store"))
	if w.Traced.Tracer.Provider == nil || w.Traced.Tracer.Provider.Name != "app.NewTracerProvider" || w.Traced.Tracer.Binding != "sole candidate" {
		t.Fatalf("tracer resolved to %s (%s), want app.NewTracerProvider as the sole in-scope exact candidate", w.Traced.Tracer.Key, w.Traced.Tracer.Binding)
	}
}

// TestTraceFailureIsReportedOnce: a wrapper that failed on its tracer
// provider stays failed for every later consumer, so two consumers of the
// interface produce one diagnostic, not two.
func TestTraceFailureIsReportedOnce(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewServer", "NewWorker"),
		[]load.RootDecl{rootDecl(pkg, "Server"), rootDecl(pkg, "Worker")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	if !strings.Contains(diags[0].String(), "no provider for example.com/app.TracerProvider") {
		t.Fatalf("diagnostic:\n%s", diags[0])
	}
}

// TestTwoTracedInterfacesBehindOneImplementation: two wrappers around one
// node, each with its own span names, reported in declaration order
// whatever order the roots reached them in.
func TestTwoTracedInterfacesBehindOneImplementation(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewSDK", "NewServer", "NewProber"),
		[]load.RootDecl{rootDecl(pkg, "Server"), rootDecl(pkg, "Prober")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	store, pinger := traceDecl(pkg, "Store"), traceDecl(pkg, "Pinger")
	store.Pos.Line, pinger.Pos.Line = 20, 10 // declared in the opposite order to the roots that reach them
	in.Spec.Traces = []load.TraceDecl{store, pinger}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Traced) != 2 || resolved.Traced[0].Key.Type != "example.com/app.Pinger" || resolved.Traced[1].Key.Type != "example.com/app.Store" {
		t.Fatalf("Resolved.Traced = %v, want Pinger then Store, by declaration position", resolved.Traced)
	}
	if resolved.Traced[0].Traced.Inner != resolved.Traced[1].Traced.Inner {
		t.Fatal("the two wrappers must share the one *Postgres node")
	}
}

// TestTraceWrapsOverrideInTestVariant: an Override replaces the
// implementation, not the marker, so NewTestApp traces the mock through
// the same wrapper.
func TestTraceWrapsOverrideInTestVariant(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewMemory", "NewSDK", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)
	in.ExtraBinds = []load.BindDecl{storeBind(pkg, "Memory")}

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	w := findTraced(t, resolved, namedKey(pkg, "Store"))
	if w.Traced.Inner.Key != ptrKey(pkg, "Memory") {
		t.Fatalf("wrapper wraps %s, want the override *Memory", w.Traced.Inner.Key)
	}
}

// TestTraceWrapsSuppliedValue: a servo.Value for the interface still wins
// the resolution of the interface itself; the wrapper then sits around
// the supplied node.
func TestTraceWrapsSuppliedValue(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewSDK", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, nil)
	in.Spec.Values = []load.ValueDecl{{Key: namedKey(pkg, "Store"), Type: namedType(pkg, "Store"), Pos: token.Position{Filename: "spec.go", Line: 11}}}
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	w := findTraced(t, resolved, namedKey(pkg, "Store"))
	if w.Traced.Inner.Kind != NodeSupplied {
		t.Fatalf("wrapper wraps a %d node, want the supplied value", w.Traced.Inner.Kind)
	}
	if len(resolved.Supplied) != 1 {
		t.Fatalf("supplied values = %d, want the one the wrapper consumes", len(resolved.Supplied))
	}
}

func TestTraceCycleThroughTracerProviderIsReported(t *testing.T) {
	pkg, all := checkTraceFixture(t)
	in := baseInput(pkg, providers(t, all, "NewLogger", "NewPostgres", "NewLoopSDK", "NewServer"),
		[]load.RootDecl{rootDecl(pkg, "Server")}, []load.BindDecl{storeBind(pkg, "Postgres")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Store")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) == 0 {
		t.Fatalf("got resolved=%v diags=%d, want a cycle diagnostic", resolved != nil, len(diags))
	}
	msg := diagText(diags)
	if !strings.Contains(msg, "cycle") || !strings.Contains(msg, "the servo.Trace wrapper for example.com/app.Store") {
		t.Fatalf("want a cycle diagnostic naming the wrapper:\n%s", msg)
	}
}

func TestTraceOnAccessorInterfaceIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Rooms")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	if !strings.Contains(diags[0].String(), "example.com/scoped.Rooms is a scope accessor interface") {
		t.Fatalf("diagnostic:\n%s", diags[0])
	}
}

// TestTraceBorrowedByScopeIsASingleton: a scoped member taking a traced
// singleton interface gets the wrapper as a borrowed singleton, built
// once by New like the logger behind it.
func TestTraceBorrowedByScopeIsASingleton(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "Diary", "Diaries")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Logs")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	w := findTraced(t, resolved, namedKey(pkg, "Logs"))
	if w.Scope != nil {
		t.Fatalf("wrapper is a member of %s, want a singleton the scope borrows", w.Scope.Name)
	}
	var inOrder bool
	for _, n := range resolved.Order {
		inOrder = inOrder || n == w
	}
	if !inOrder {
		t.Fatal("wrapper is missing from Resolved.Order: nothing would construct it")
	}
	diary := resolved.ByKey[ptrKey(pkg, "Diary")]
	if diary.Scope == nil || diary.Deps[1] != w {
		t.Fatalf("Diary should be a scoped member holding the wrapper; deps = %v", diary.Deps)
	}
}

func TestTraceOnScopeKeyTypeIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "RoomKey")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	if !strings.Contains(diags[0].String(), "example.com/scoped.RoomKey is a scope key type") {
		t.Fatalf("diagnostic:\n%s", diags[0])
	}
}

// TestTraceWithScopedTracerProviderIsRejected: the provider the wrapper
// takes can be scoped too, if its constructor takes the key, and that is
// refused with the same message a scoped implementation gets.
func TestTraceWithScopedTracerProviderIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "Diary", "Diaries")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Logs")}
	in.Trace = &TraceInput{ProviderKey: ptrKey(pkg, "ScopedSDK"), ProviderType: ptrType(pkg, "ScopedSDK")}

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	if !strings.Contains(diags[0].String(), "example.com/scoped.Logs resolves to *example.com/scoped.ScopedSDK, which is scoped") {
		t.Fatalf("diagnostic:\n%s", diags[0])
	}
}

// TestWideningThroughAWrapperNamesTheWrapper: a diagnostic whose
// needed-by chain passes through a wrapper reads the same way traversal's
// own chains do, at the declaration's position.
func TestWideningThroughAWrapperNamesTheWrapper(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "PgHolder")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "TracedStore")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) == 0 {
		t.Fatalf("got resolved=%v diags=%d, want the widening diagnostic", resolved != nil, len(diags))
	}
	msg := diagText(diags)
	for _, want := range []string{
		"is scoped, but *example.com/scoped.Pg is a singleton that depends on it",
		"needed by the servo.Trace wrapper for example.com/scoped.TracedStore  spec.go:12",
		"needed by *example.com/scoped.PgHolder",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}

func TestTraceOnScopedImplementationIsRejected(t *testing.T) {
	pkg, fset, pkgs, all := checkScopeFixture(t)
	in := scopeInput(t, pkg, fset, pkgs, all,
		[]load.RootDecl{rootDecl(pkg, "Server")},
		[]load.ScopeDecl{scopeDecl(pkg, "Room", "Rooms"), scopeDecl(pkg, "Booth", "Booths")})
	in.Spec.Traces = []load.TraceDecl{traceDecl(pkg, "Chatter")}
	in.Trace = traceInput(pkg)

	resolved, diags := Resolve(in)
	if resolved != nil || len(diags) != 1 {
		t.Fatalf("got resolved=%v diags=%d, want exactly one diagnostic:\n%s", resolved != nil, len(diags), diagText(diags))
	}
	msg := diags[0].String()
	for _, want := range []string{
		"servo.Trace[example.com/scoped.Chatter]: example.com/scoped.Chatter resolves to *example.com/scoped.Room, which is scoped (scope example.com/scoped.RoomKey)",
		"this release does not wrap scoped instances",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
}
