package resolve

import (
	"go/token"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

// TestBindToAForeignTypeWithTwoConstructorsIsAForeignAmbiguity: a Bind
// settles which type an interface resolves to, not which of a dependency's
// constructors builds it. With two of those and none of the module's own,
// the reader gets the foreign advice — write a constructor, or supply a
// Value — never "delete all but one" about code they do not own.
func TestBindToAForeignTypeWithTwoConstructorsIsAForeignAmbiguity(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.org/lib", `package lib

type Logger struct{}

func (*Logger) Log(msg string) {}

func New() *Logger     { return &Logger{} }
func Default() *Logger { return &Logger{} }
`},
		pkgSrc{"example.com/mod/app", `package app

type Logs interface{ Log(msg string) }

type Server struct{}

func NewServer(l Logs) *Server { return &Server{} }
`})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.Binds = []load.BindDecl{{
		Iface: graph.NewKey(m.typ("example.com/mod/app", "Logs"), ""), IfaceType: m.typ("example.com/mod/app", "Logs"),
		Concrete: graph.NewKey(m.ptr("example.org/lib", "Logger"), ""), ConcreteType: m.ptr("example.org/lib", "Logger"),
	}}

	msg := onlyDiagnostic(t, in).Message
	for _, want := range []string{
		"servo: ambiguous provider for *example.org/lib.Logger\n",
		"2 functions outside this module produce *lib.Logger, and servo will not pick one of\n  them for you:",
		"lib.New ", "lib.Default ",
		"func NewLogger(...) *lib.Logger",
		"servo.Value[*lib.Logger]()",
		"\"example.org/lib\"",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Delete all but one") {
		t.Errorf("diagnostic tells the reader to delete functions they do not own:\n%s", msg)
	}
}

// TestForeignInterfaceConstructorsWithNoImplementationOfOurOwn: several
// outside functions returning an interface exactly wait for the module's
// own implementations, and when there are none they are the same foreign
// ambiguity an exact type gets.
func TestForeignInterfaceConstructorsWithNoImplementationOfOurOwn(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", libSrc}, pkgSrc{"example.com/mod/app", `package app

import "example.org/lib"

type Server struct{}

func NewServer(h lib.Handler) *Server { return &Server{} }
`})
	msg := onlyDiagnostic(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))).Message
	for _, want := range []string{
		"servo: ambiguous provider for example.org/lib.Handler\n",
		"2 functions outside this module produce lib.Handler",
		"lib.HandlerA ", "lib.HandlerB ",
		"func NewHandler(...) lib.Handler",
		"servo.Value[lib.Handler]()",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "servo.Bind[") {
		t.Errorf("diagnostic suggests a Bind, but nothing in the module implements lib.Handler:\n%s", msg)
	}
}

// TestRootDeclaredByKeyAlone: a request that carries no type — a Spec
// built by hand — is matched by its key. A constructor for the key still
// provides it, context.Context is still the construction context rather
// than a search, and a key nothing provides is reported without the
// near-miss lines, which need a type to look for.
func TestRootDeclaredByKeyAlone(t *testing.T) {
	pkg, all := checkFixture(t)
	candidates := []*graph.Provider{findProvider(t, all, "NewLogger")}
	roots := []load.RootDecl{
		{Key: ptrKey(pkg, "Logger"), Pos: token.Position{Filename: "spec.go", Line: 9}},
		{Key: graph.Key{Type: "context.Context"}, Pos: token.Position{Filename: "spec.go", Line: 10}},
	}
	resolved, diags := Resolve(baseInput(pkg, candidates, roots, nil))
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics:\n%s", diagText(diags))
	}
	if len(resolved.Roots) != 2 {
		t.Fatalf("got %d roots, want 2", len(resolved.Roots))
	}
	if got := resolved.Roots[0]; got.Provider == nil || got.Provider.Name != "app.NewLogger" {
		t.Errorf("*app.Logger root = %+v, want the one built by app.NewLogger", got)
	}
	if got := resolved.Roots[1]; got.Kind != NodeContext || got.Provider != nil {
		t.Errorf("context.Context root = %+v, want the construction context", got)
	}

	roots = []load.RootDecl{{Key: ptrKey(pkg, "Missing"), Pos: token.Position{Filename: "spec.go", Line: 9}}}
	msg := onlyDiagnostic(t, baseInput(pkg, candidates, roots, nil)).Message
	if want := "servo: no provider for *example.com/app.Missing\n  root  spec.go:9\n"; msg != want {
		t.Errorf("message = %q, want exactly %q", msg, want)
	}
}
