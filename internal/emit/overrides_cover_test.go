package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// fallibleOverrideSrc has a suppliable Override whose constructor takes a
// dependency and can fail, after a Logger with something to stop has been
// built. The mock's constructor always fails; everything logs.
const fallibleOverrideSrc = `
package app

import (
	"context"
	"errors"
)

var Log []string

type Store interface{ Get(key string) string }

type Logger struct{}

func NewLogger() *Logger { Log = append(Log, "build Logger"); return &Logger{} }
func (*Logger) Stop(ctx context.Context) error { Log = append(Log, "stop Logger"); return nil }

var ErrMock = errors.New("mock: no fixture data")

type StoreMock struct{ l *Logger }

func NewStoreMock(l *Logger) (*StoreMock, error) {
	Log = append(Log, "build StoreMock")
	return nil, ErrMock
}
func (*StoreMock) Get(key string) string { return key }

type Server struct{ s Store }

func NewServer(s Store) *Server { return &Server{s: s} }
func (s *Server) Lookup(key string) string { return s.s.Get(key) }
`

// buildFallibleOverride resolves fallibleOverrideSrc's override variant.
func buildFallibleOverride(t *testing.T) []byte {
	t.Helper()
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:     []string{"Server"},
		overrides: [][2]string{{"Store", "*StoreMock"}},
	}, fixturePkg{"example.com/app", fallibleOverrideSrc})
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return out
}

// TestEmitSuppliableOverrideWithAFallibleConstructor: the constructor-built
// half of a suppliable Override is ordinary construction — its arguments
// are the nodes built before it, and a failure unwinds what was built
// before it and returns its error — only nested under the TestValues
// check, so a supplied implementation skips all of it.
func TestEmitSuppliableOverrideWithAFallibleConstructor(t *testing.T) {
	out := buildFallibleOverride(t)
	want := "\tvar storeOverride Store\n\tif v.Store != nil {\n\t\tstoreOverride = v.Store\n\t} else {\n" +
		"\t\tstoreMock, err := NewStoreMock(logger)\n\t\tif err != nil {\n\t\t\ta.unwind(context.WithoutCancel(ctx), undo)\n\t\t\treturn nil, err\n\t\t}\n" +
		"\t\ta.storeMock = storeMock\n\t\tstoreOverride = storeMock\n\t}\n\ta.storeOverride = storeOverride\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("generated source missing\n%s\n---\n%s", want, out)
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", fallibleOverrideSrc})
}

// TestEmitSuppliableOverrideWithAFallibleConstructorRuns drives both
// paths: NewTestApp calls the failing constructor, stops the Logger it had
// already built and returns the constructor's own error; NewTestAppWith
// with the Store set never calls it, so nothing fails.
func TestEmitSuppliableOverrideWithAFallibleConstructorRuns(t *testing.T) {
	out := buildFallibleOverride(t)
	runGenerated(t, "app", out, map[string]string{
		"app/app.go": fallibleOverrideSrc,
		"app/override_test.go": `package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeStore struct{}

func (fakeStore) Get(key string) string { return "fake:" + key }

func TestFallibleOverride(t *testing.T) {
	app, err := NewTestApp(context.Background())
	if app != nil || !errors.Is(err, ErrMock) {
		t.Fatalf("NewTestApp = %v, %v; want nil and the mock constructor's error", app, err)
	}
	if got, want := strings.Join(Log, ", "), "build Logger, build StoreMock, stop Logger"; got != want {
		t.Fatalf("NewTestApp: got  %s\nwant %s", got, want)
	}

	Log = nil
	app, err = NewTestAppWith(context.Background(), TestValues{Store: fakeStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Log, ", "); got != "build Logger" {
		t.Errorf("NewTestAppWith: got %s, want only the Logger built", got)
	}
	if got := app.server.Lookup("k"); got != "fake:k" || app.storeMock != nil {
		t.Errorf("NewTestAppWith: Lookup = %q, storeMock %v; want the supplied Store and no mock", got, app.storeMock)
	}
}
`,
	})
}

// TestEmitOneNodeBehindTwoOverridesGetsOneField: two Overrides can bind
// the same concrete node to two interfaces. The node has one App field of
// its own and can be supplied as one value, so only the first Override
// gets a TestValues field; a second would emit a second interface-typed
// field and a second construction block for the one node.
func TestEmitOneNodeBehindTwoOverridesGetsOneField(t *testing.T) {
	const src = `
package app

type Store interface{ Get(key string) string }
type Getter interface{ Get(key string) string }

type Mock struct{}

func NewMock() *Mock { return &Mock{} }
func (*Mock) Get(key string) string { return key }
`
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:     []string{"Mock"},
		overrides: [][2]string{{"Store", "*Mock"}, {"Getter", "*Mock"}},
	}, fixturePkg{"example.com/app", src})
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	if !strings.Contains(gen, "\tStore Store\n}") {
		t.Errorf("the first Override has no TestValues field:\n%s", gen)
	}
	for _, unwanted := range []string{"Getter Getter", "getterOverride"} {
		if strings.Contains(gen, unwanted) {
			t.Errorf("the second Override of the same node got %q:\n%s", unwanted, gen)
		}
	}
	if n := strings.Count(gen, "mock := NewMock()"); n != 1 {
		t.Errorf("the node is constructed %d times, want once:\n%s", n, gen)
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
}

// TestSuppliableJudgesTheTracerAWrapperTakes: a trace wrapper takes two
// nodes, the implementation and the trace.TracerProvider. A supplied value
// has only the overridden interface's methods, so when the overridden
// node is the wrapper's tracer it can be supplied only as the
// TracerProvider itself, recognised by the OpenTelemetry package's path.
func TestSuppliableJudgesTheTracerAWrapperTakes(t *testing.T) {
	fset := token.NewFileSet()
	check := func(path, src string) *types.Package {
		t.Helper()
		f, err := parser.ParseFile(fset, path+".go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := (&types.Config{}).Check(path, fset, []*ast.File{f}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return pkg
	}
	tracePkg := check(graph.OtelTracePackagePath, "package trace\ntype TracerProvider interface{ Tracer(name string) }\n")
	appPkg := check("example.com/app", "package app\ntype Store interface{ Get() }\ntype Fake struct{}\ntype TracerProvider interface{ Tracer(name string) }\n")
	storeT := appPkg.Scope().Lookup("Store").Type()
	fakeT := types.NewPointer(appPkg.Scope().Lookup("Fake").Type())
	concrete := graph.NewKey(fakeT, "")

	n := &resolve.Node{Kind: resolve.NodeProvider, Key: concrete, Provider: &graph.Provider{Result: concrete, ResultType: fakeT}}
	inner := &resolve.Node{Kind: resolve.NodeProvider, Provider: &graph.Provider{}}
	wrapper := &resolve.Node{Kind: resolve.NodeTraced, Deps: []*resolve.Node{inner, n}, Traced: &resolve.Traced{IfaceType: storeT, Inner: inner, Tracer: n}}
	resolved := &resolve.Resolved{Order: []*resolve.Node{inner, n, wrapper}}

	for _, c := range []struct {
		name  string
		iface types.Type
		want  bool
	}{
		{"overridden as the TracerProvider", tracePkg.Scope().Lookup("TracerProvider").Type(), true},
		{"overridden as an interface the wrapper does not take it as", storeT, false},
		// Same name and methods, but not OpenTelemetry's type.
		{"overridden as a look-alike TracerProvider", appPkg.Scope().Lookup("TracerProvider").Type(), false},
	} {
		ov := load.BindDecl{Iface: graph.NewKey(c.iface, ""), IfaceType: c.iface, Concrete: concrete, ConcreteType: fakeT}
		if got := (&emitter{resolved: resolved}).suppliable(n, ov); got != c.want {
			t.Errorf("%s: suppliable = %v, want %v", c.name, got, c.want)
		}
	}
}
