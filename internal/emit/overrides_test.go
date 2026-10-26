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
	"github.com/okian/servo/v3/internal/route"
)

// overrideAppSrc is the shape every mocking guide starts from: a Server
// that takes a Store interface, and a mock with a dependency-free
// constructor that servo.Override swaps in for the test graph. The mock
// counts how often its constructor ran.
const overrideAppSrc = `
package app

type Store interface{ Get(key string) string }

var MockBuilt int

type StoreMock struct{ Prefix string }

func NewStoreMock() *StoreMock { MockBuilt++; return &StoreMock{Prefix: "mock:"} }
func (m *StoreMock) Get(key string) string { return m.Prefix + key }

type Server struct{ s Store }

func NewServer(s Store) *Server { return &Server{s: s} }
func (s *Server) Lookup(key string) string { return s.s.Get(key) }
`

// TestEmitOverrideCanBeSuppliedPerCall: an Override used to be built by
// its constructor alone, so a mock could not be made with the test's own
// *testing.T and every test shared one shape of it. TestValues now carries
// a field per suppliable Override, typed as the interface: nil builds the
// Override as before, set is used as is. The concrete field keeps its name
// and type, so a test reaching into app.storeMock compiles as it did.
func TestEmitOverrideCanBeSuppliedPerCall(t *testing.T) {
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:     []string{"Server"},
		overrides: [][2]string{{"Store", "*StoreMock"}},
	}, fixturePkg{"example.com/app", overrideAppSrc})
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		"\t// Store, when set, is the Store every consumer gets, in place of\n\t// the *StoreMock the Override would build; nothing in its\n",
		"\tStore Store\n}",
		"func NewTestApp(ctx context.Context) (*TestApp, error) {\n\treturn NewTestAppWith(ctx, TestValues{})\n}",
		"func NewTestAppWith(ctx context.Context, v TestValues) (*TestApp, error) {",
		"\tvar storeOverride Store\n\tif v.Store != nil {\n\t\tstoreOverride = v.Store\n\t} else {\n\t\tstoreMock := NewStoreMock()\n\t\ta.storeMock = storeMock\n\t\tstoreOverride = storeMock\n\t}\n\ta.storeOverride = storeOverride\n",
		"server := NewServer(storeOverride)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, src)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", overrideAppSrc})

	// The production graph has no Override and so no TestValues: nothing
	// about it changes.
	prod, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if strings.Contains(string(prod), "Values") || strings.Contains(string(prod), "Override") {
		t.Errorf("the production variant grew override machinery:\n%s", prod)
	}
}

// TestEmitOverrideSuppliedPerCallRuns drives both paths: NewTestApp builds
// the mock through its constructor, as it always did; NewTestAppWith with
// the field set uses the test's own instance and never calls the
// constructor, leaving the concrete field nil.
func TestEmitOverrideSuppliedPerCallRuns(t *testing.T) {
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:     []string{"Server"},
		overrides: [][2]string{{"Store", "*StoreMock"}},
	}, fixturePkg{"example.com/app", overrideAppSrc})
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	runGenerated(t, "app", out, map[string]string{
		"app/app.go": overrideAppSrc,
		"app/override_test.go": `package app

import (
	"context"
	"testing"
)

func TestOverride(t *testing.T) {
	app, err := NewTestApp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := app.server.Lookup("k"); got != "mock:k" || MockBuilt != 1 || app.storeMock == nil {
		t.Fatalf("NewTestApp: Lookup = %q, built %d, field %v; want the constructor's mock", got, MockBuilt, app.storeMock)
	}

	mine := &StoreMock{Prefix: "mine:"}
	app, err = NewTestAppWith(context.Background(), TestValues{Store: mine})
	if err != nil {
		t.Fatal(err)
	}
	if got := app.server.Lookup("k"); got != "mine:k" || MockBuilt != 1 || app.storeMock != nil || app.storeOverride != mine {
		t.Fatalf("NewTestAppWith: Lookup = %q, built %d, field %v; want the supplied mock and no constructor call", got, MockBuilt, app.storeMock)
	}
}
`,
	})
}

// TestEmitOverrideStaysConstructorOnlyWhereSupplyingCannotWork: a supplied
// implementation has only the interface's methods, so an Override whose
// node has a lifecycle — something servo would have to call on whatever
// the test handed in — or a consumer of the concrete type keeps its
// constructor and gets no field, generating exactly what it did before.
func TestEmitOverrideStaysConstructorOnlyWhereSupplyingCannotWork(t *testing.T) {
	for name, src := range map[string]string{
		"a lifecycle": `
package app

import "context"

type Store interface{ Get(key string) string }

type StoreMock struct{}

func NewStoreMock() *StoreMock { return &StoreMock{} }
func (*StoreMock) Get(key string) string { return key }
func (*StoreMock) Stop(ctx context.Context) error { return nil }

type Server struct{}

func NewServer(s Store) *Server { return &Server{} }
`,
		"a concrete consumer": `
package app

type Store interface{ Get(key string) string }

type StoreMock struct{}

func NewStoreMock() *StoreMock { return &StoreMock{} }
func (*StoreMock) Get(key string) string { return key }

type Inspector struct{}

func NewInspector(m *StoreMock) *Inspector { return &Inspector{} }

type Server struct{}

func NewServer(s Store, i *Inspector) *Server { return &Server{} }
`,
	} {
		t.Run(name, func(t *testing.T) {
			resolved, spec := buildFixture(t, fixtureSpec{
				roots:     []string{"Server"},
				overrides: [][2]string{{"Store", "*StoreMock"}},
			}, fixturePkg{"example.com/app", src})
			out, err := Emit(resolved, spec, true)
			if err != nil {
				t.Fatalf("Emit: %v", err)
			}
			gen := string(out)
			if strings.Contains(gen, "TestValues") || strings.Contains(gen, "storeOverride") {
				t.Errorf("the Override cannot be supplied, so it must get no field:\n%s", gen)
			}
			if !strings.Contains(gen, "storeMock := NewStoreMock()") {
				t.Errorf("the Override is no longer built by its constructor:\n%s", gen)
			}
			typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
		})
	}
}

// scopedOverrideSrc has a scoped Room borrow the overridden Store. A scope
// entry reads what it borrows off the App, so the supplied value has to be
// on the App too, in the interface-typed field.
const scopedOverrideSrc = `
package app

import "context"

type Store interface{ Get(key string) string }

type StoreMock struct{}

func NewStoreMock() *StoreMock { return &StoreMock{} }
func (*StoreMock) Get(key string) string { return key }

type RoomKey string

type Room struct{}

func NewRoom(k RoomKey, s Store) *Room { return &Room{} }
func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}

func NewServer(r Rooms, s Store) *Server { return &Server{} }
`

// TestEmitOverrideSuppliedToAScopeAndValuesTogether covers a scoped
// consumer of the interface and a servo.Value beside the Override: the
// entry borrows the interface-typed field, and TestValues carries both.
func TestEmitOverrideSuppliedToAScopeAndValuesTogether(t *testing.T) {
	src := scopedOverrideSrc + "\ntype Flags struct{ Verbose bool }\n\ntype Audit struct{}\n\nfunc NewAudit(f Flags) *Audit { return &Audit{} }\n"
	resolved, spec := buildFixture(t, fixtureSpec{
		roots:     []string{"Server", "Audit"},
		values:    []string{"Flags"},
		overrides: [][2]string{{"Store", "*StoreMock"}},
		scopes:    [][2]string{{"Room", "Rooms"}},
	}, fixturePkg{"example.com/app", src})
	out, err := Emit(resolved, spec, true)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		"type TestValues struct {\n\tFlags Flags\n",
		"\tStore Store\n}",
		"NewRoom(e.key, a.storeOverride)",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", src})
}

// TestSuppliableNeedsEveryConsumerToTakeTheInterface walks each consumer
// kind the rule has to judge, on hand-built nodes: a provider or a route
// handler taking the interface, or a trace wrapper for it, is fine; the
// same consumer taking the concrete type is not, nor is the node acting as
// middleware, an extractor, the HTTP config or a scope key's dependency,
// nor a node with anything in its lifecycle.
func TestSuppliableNeedsEveryConsumerToTakeTheInterface(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", "package app\ntype Store interface{ Get() }\ntype Other interface{ Get() }\ntype Mock struct{}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	storeT, otherT := pkg.Scope().Lookup("Store").Type(), pkg.Scope().Lookup("Other").Type()
	mockT := types.NewPointer(pkg.Scope().Lookup("Mock").Type())
	iface, concrete := graph.NewKey(storeT, ""), graph.NewKey(mockT, "")
	ov := load.BindDecl{Iface: iface, IfaceType: storeT, Concrete: concrete, ConcreteType: mockT}

	newNode := func() *resolve.Node {
		return &resolve.Node{Kind: resolve.NodeProvider, Key: concrete, Provider: &graph.Provider{Result: concrete, ResultType: mockT}}
	}
	consumer := func(n *resolve.Node, param graph.Key) *resolve.Node {
		return &resolve.Node{Kind: resolve.NodeProvider, Deps: []*resolve.Node{n}, Provider: &graph.Provider{Params: []graph.Key{param}}}
	}
	cases := []struct {
		name string
		want bool
		mk   func(n *resolve.Node) *resolve.Resolved
	}{
		{"provider takes the interface", true, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n, consumer(n, iface)}}
		}},
		{"provider takes the concrete type", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n, consumer(n, concrete)}}
		}},
		{"trace wrapper for the interface", true, func(n *resolve.Node) *resolve.Resolved {
			w := &resolve.Node{Kind: resolve.NodeTraced, Deps: []*resolve.Node{n, {}}, Traced: &resolve.Traced{IfaceType: storeT, Inner: n}}
			return &resolve.Resolved{Order: []*resolve.Node{n, w}}
		}},
		{"trace wrapper for another interface", false, func(n *resolve.Node) *resolve.Resolved {
			w := &resolve.Node{Kind: resolve.NodeTraced, Deps: []*resolve.Node{n, {}}, Traced: &resolve.Traced{IfaceType: otherT, Inner: n}}
			return &resolve.Resolved{Order: []*resolve.Node{n, w}}
		}},
		{"route handler takes the interface", true, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, HTTP: &resolve.HTTPPlan{Config: &resolve.Node{}, Routes: []*resolve.HTTPRoute{{Route: &route.Route{Deps: []graph.Key{iface}}, Args: []resolve.HTTPArg{{Node: n}}}}}}
		}},
		{"route handler takes the concrete type", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, HTTP: &resolve.HTTPPlan{Config: &resolve.Node{}, Routes: []*resolve.HTTPRoute{{Route: &route.Route{Deps: []graph.Key{concrete}}, Args: []resolve.HTTPArg{{Node: n}}}}}}
		}},
		{"middleware", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, HTTP: &resolve.HTTPPlan{Config: &resolve.Node{}, Uses: []*resolve.HTTPUse{{Node: n}}}}
		}},
		{"extractor", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, HTTP: &resolve.HTTPPlan{Config: &resolve.Node{}, Extractors: []*resolve.HTTPExtractor{{Node: n}}}}
		}},
		{"HTTP config", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, HTTP: &resolve.HTTPPlan{Config: n}}
		}},
		{"scope key extractor dependency", false, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, Scopes: []*resolve.Scope{{Roots: []*resolve.ScopeRoot{{ExtractorDeps: []*resolve.Node{n}}}}}}
		}},
		{"scoped member takes the interface", true, func(n *resolve.Node) *resolve.Resolved {
			return &resolve.Resolved{Order: []*resolve.Node{n}, Scopes: []*resolve.Scope{{Order: []*resolve.Node{consumer(n, iface)}}}}
		}},
		{"a capability", false, func(n *resolve.Node) *resolve.Resolved {
			n.Capabilities = []string{"Healther"}
			return &resolve.Resolved{Order: []*resolve.Node{n, consumer(n, iface)}}
		}},
		{"a cleanup func", false, func(n *resolve.Node) *resolve.Resolved {
			n.Provider.HasCleanup = true
			return &resolve.Resolved{Order: []*resolve.Node{n, consumer(n, iface)}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := newNode()
			e := &emitter{resolved: tc.mk(n)}
			if got := e.suppliable(n, ov); got != tc.want {
				t.Errorf("suppliable = %v, want %v", got, tc.want)
			}
		})
	}
}
