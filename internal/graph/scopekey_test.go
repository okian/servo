package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const scopeKeyFixtureSrc = `
package fixture

import "context"

type RoomKey string
type Cfg struct{}

// Good is the canonical shape: blank receiver, context first, (K, error).
type Good struct{}
func (_ *Good) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

// WithDeps takes extra parameters, which resolve as ordinary graph edges.
type WithDeps struct{}
func (_ *WithDeps) ScopeKey(ctx context.Context, c *Cfg) (RoomKey, error) { return "", nil }

// Plain has no ScopeKey at all — the overwhelmingly common case.
type Plain struct{}

// NoCtx's first parameter is not a context.
type NoCtx struct{}
func (_ *NoCtx) ScopeKey(k RoomKey) (RoomKey, error) { return "", nil }

// NoParams has no parameters at all.
type NoParams struct{}
func (_ *NoParams) ScopeKey() (RoomKey, error) { return "", nil }

// NoError drops the error result, which is what would let a missing key
// become the zero RoomKey.
type NoError struct{}
func (_ *NoError) ScopeKey(ctx context.Context) RoomKey { return "" }

// WrongSecond returns something that is not the error interface.
type WrongSecond struct{}
func (_ *WrongSecond) ScopeKey(ctx context.Context) (RoomKey, bool) { return "", false }

// UndefinedKey returns a bare string rather than a defined type.
type UndefinedKey struct{}
func (_ *UndefinedKey) ScopeKey(ctx context.Context) (string, error) { return "", nil }

// IfaceKey returns an interface, whose dynamic types would never compare
// equal across callers.
type Named interface{ Name() string }
type IfaceKey struct{}
func (_ *IfaceKey) ScopeKey(ctx context.Context) (Named, error) { return nil, nil }

// UncomparableKey cannot key a map.
type SliceKey []string
type Uncomparable struct{}
func (_ *Uncomparable) ScopeKey(ctx context.Context) (SliceKey, error) { return nil, nil }

// Variadic's trailing parameter is a slice, which is never resolvable.
type Variadic struct{}
func (_ *Variadic) ScopeKey(ctx context.Context, extra ...*Cfg) (RoomKey, error) { return "", nil }

// ValueRecv declares ScopeKey on the value, so calling it through a typed
// nil *ValueRecv would dereference that nil.
type ValueRecv struct{}
func (_ ValueRecv) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

// NamedRecv's body can reach its own receiver.
type NamedRecv struct{}
func (r *NamedRecv) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

// Anonymous receiver: unnamed, so equally unreachable from the body.
type AnonRecv struct{}
func (*AnonRecv) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }

// Embedded promotes Good's ScopeKey rather than declaring its own.
type Embedded struct{ *Good }
`

func checkScopeKeyFixture(t *testing.T) (*types.Package, *token.FileSet, []*packages.Package) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", scopeKeyFixtureSrc, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: newPkgImporter(servoPkg)}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, err := conf.Check("example.com/fixture", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	pkgs := []*packages.Package{{
		Name: "fixture", PkgPath: "example.com/fixture",
		Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info,
	}}
	return pkg, fset, pkgs
}

func ptrTo(pkg *types.Package, name string) types.Type {
	return types.NewPointer(pkg.Scope().Lookup(name).Type())
}

func TestFindScopeKeyAccepts(t *testing.T) {
	pkg, fset, _ := checkScopeKeyFixture(t)

	for _, tc := range []struct {
		typeName string
		wantDeps int
	}{
		{"Good", 0},
		{"WithDeps", 1},
		{"AnonRecv", 0},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			sk, err := FindScopeKey(fset, ptrTo(pkg, tc.typeName))
			if err != nil {
				t.Fatalf("FindScopeKey: %v", err)
			}
			if sk == nil {
				t.Fatal("FindScopeKey found nothing")
			}
			if got := TypeString(sk.KeyType); got != "example.com/fixture.RoomKey" {
				t.Fatalf("key type = %s", got)
			}
			if len(sk.Params) != tc.wantDeps {
				t.Fatalf("got %d extractor deps, want %d", len(sk.Params), tc.wantDeps)
			}
		})
	}
}

// TestFindScopeKeyIgnoresTypesWithoutOne covers the answer every ordinary
// node gets, and the deliberate refusal to follow a promoted method: a
// ScopeKey reached through an embedded field would be called on a typed
// nil whose embedded pointer is itself nil.
func TestFindScopeKeyIgnoresTypesWithoutOne(t *testing.T) {
	pkg, fset, _ := checkScopeKeyFixture(t)

	for _, name := range []string{"Plain", "Embedded"} {
		sk, err := FindScopeKey(fset, ptrTo(pkg, name))
		if err != nil {
			t.Fatalf("%s: FindScopeKey: %v", name, err)
		}
		if sk != nil {
			t.Fatalf("%s: found a ScopeKey, want none", name)
		}
	}
	// A type with no named type underneath it at all.
	if sk, err := FindScopeKey(fset, types.Typ[types.Int]); sk != nil || err != nil {
		t.Fatalf("int: got sk=%v err=%v, want both nil", sk, err)
	}
}

func TestFindScopeKeyRejects(t *testing.T) {
	pkg, fset, _ := checkScopeKeyFixture(t)

	for _, tc := range []struct {
		typeName string
		wantMsg  string
	}{
		{"NoCtx", "first parameter must be context.Context"},
		{"NoParams", "first parameter must be context.Context"},
		{"NoError", "must return exactly (K, error)"},
		{"WrongSecond", "must return exactly (K, error)"},
		{"UndefinedKey", "not a defined type"},
		{"IfaceKey", "is an interface"},
		{"Uncomparable", "not comparable"},
		{"Variadic", "must not be variadic"},
		{"ValueRecv", "must be declared on the pointer receiver"},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			sk, err := FindScopeKey(fset, ptrTo(pkg, tc.typeName))
			if sk != nil {
				t.Fatal("expected no ScopeKey")
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}

// TestValueReceiverIsFineOnAValueType is the other half of the pointer-
// receiver rule: the rejection is about calling through a nil pointer, not
// about value receivers as such.
func TestValueReceiverIsFineOnAValueType(t *testing.T) {
	pkg, fset, _ := checkScopeKeyFixture(t)
	sk, err := FindScopeKey(fset, pkg.Scope().Lookup("ValueRecv").Type())
	if err != nil || sk == nil {
		t.Fatalf("got sk=%v err=%v, want an accepted extractor", sk, err)
	}
}

func TestReceiverIsBlank(t *testing.T) {
	pkg, _, pkgs := checkScopeKeyFixture(t)

	for _, tc := range []struct {
		typeName string
		want     bool
	}{
		{"Good", true},
		{"AnonRecv", true},
		{"NamedRecv", false},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			named := pkg.Scope().Lookup(tc.typeName).Type().(*types.Named)
			var m *types.Func
			for i := 0; i < named.NumMethods(); i++ {
				if named.Method(i).Name() == ScopeKeyMethodName {
					m = named.Method(i)
				}
			}
			if m == nil {
				t.Fatal("no ScopeKey method on the fixture type")
			}
			decl := FuncDeclOf(pkgs, m)
			if decl == nil {
				t.Fatal("FuncDeclOf found no declaration")
			}
			if got := ReceiverIsBlank(decl); got != tc.want {
				t.Fatalf("ReceiverIsBlank = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFuncDeclOfMisses covers the two ways the lookup legitimately finds
// nothing: a package loaded without syntax, and a function from a package
// that isn't in the set at all.
func TestFuncDeclOfMisses(t *testing.T) {
	pkg, _, pkgs := checkScopeKeyFixture(t)
	named := pkg.Scope().Lookup("Good").Type().(*types.Named)
	m := named.Method(0)

	if got := FuncDeclOf(nil, m); got != nil {
		t.Fatal("FuncDeclOf(nil, ...) returned a declaration")
	}
	stripped := []*packages.Package{{Name: pkgs[0].Name, PkgPath: pkgs[0].PkgPath, Types: pkgs[0].Types, Fset: pkgs[0].Fset}}
	if got := FuncDeclOf(stripped, m); got != nil {
		t.Fatal("FuncDeclOf found a declaration in a package with no syntax")
	}
}

func TestReceiverIsBlankRejectsNonMethods(t *testing.T) {
	if ReceiverIsBlank(nil) {
		t.Fatal("nil decl reported blank")
	}
	if ReceiverIsBlank(&ast.FuncDecl{Name: ast.NewIdent("F")}) {
		t.Fatal("a plain function reported blank")
	}
	twoNames := &ast.FuncDecl{
		Name: ast.NewIdent("ScopeKey"),
		Recv: &ast.FieldList{List: []*ast.Field{{Names: []*ast.Ident{ast.NewIdent("a"), ast.NewIdent("b")}}}},
	}
	if ReceiverIsBlank(twoNames) {
		t.Fatal("a two-name receiver reported blank")
	}
}

func TestIsContextType(t *testing.T) {
	pkg, _, _ := checkScopeKeyFixture(t)
	named := pkg.Scope().Lookup("Good").Type().(*types.Named)
	sig := named.Method(0).Type().(*types.Signature)

	if !IsContextType(sig.Params().At(0).Type()) {
		t.Fatal("context.Context not recognized")
	}
	if IsContextType(pkg.Scope().Lookup("RoomKey").Type()) {
		t.Fatal("RoomKey recognized as a context")
	}
	if IsContextType(types.Typ[types.String]) {
		t.Fatal("string recognized as a context")
	}
}

// The three predicates below are the gates the resolver consults before it
// will say anything about a type: whether a ScopeKey is fully extractor-
// shaped, whether it is merely trying to be one, and where it is declared.
// They are exported and used from internal/resolve, so nothing in this
// package exercised them until now.

func TestScopeKeyShaped(t *testing.T) {
	pkg, _, _ := checkScopeKeyFixture(t)

	for _, tc := range []struct {
		typeName string
		want     bool
	}{
		{"Good", true},
		{"WithDeps", true},
		{"AnonRecv", true},
		{"ValueRecv", true},    // shape is fine; the receiver rule is a separate check
		{"NamedRecv", true},    // likewise
		{"Uncomparable", true}, // likewise: comparability is checked, not shape
		{"Plain", false},       // no such method at all
		{"Embedded", false},    // promoted, and promotion is never followed
		{"NoCtx", false},
		{"NoParams", false},
		{"NoError", false},
		{"WrongSecond", false},
		{"UndefinedKey", false},
		{"IfaceKey", false},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			if got := ScopeKeyShaped(ptrTo(pkg, tc.typeName)); got != tc.want {
				t.Fatalf("ScopeKeyShaped = %v, want %v", got, tc.want)
			}
		})
	}

	if ScopeKeyShaped(types.Typ[types.Int]) {
		t.Fatal("int reported as extractor-shaped")
	}
}

// TestScopeKeyLikely is the looser gate, and the looseness is the point:
// an extractor that forgot its error result is the mistake most worth
// reporting, so it has to be recognized even though it is malformed.
func TestScopeKeyLikely(t *testing.T) {
	pkg, _, _ := checkScopeKeyFixture(t)

	for _, tc := range []struct {
		typeName string
		want     bool
	}{
		{"Good", true},
		{"WithDeps", true},
		{"NoError", true}, // the one ScopeKeyShaped rejects and this must not
		{"WrongSecond", true},
		{"UndefinedKey", true},
		{"IfaceKey", true},
		{"NoCtx", false}, // no leading context — someone else's method
		{"NoParams", false},
		{"Plain", false},
		{"Embedded", false},
	} {
		t.Run(tc.typeName, func(t *testing.T) {
			if got := ScopeKeyLikely(ptrTo(pkg, tc.typeName)); got != tc.want {
				t.Fatalf("ScopeKeyLikely = %v, want %v", got, tc.want)
			}
		})
	}

	if ScopeKeyLikely(types.Typ[types.String]) {
		t.Fatal("string reported as a likely extractor")
	}
}

func TestScopeKeyPos(t *testing.T) {
	pkg, fset, _ := checkScopeKeyFixture(t)

	got := ScopeKeyPos(fset, ptrTo(pkg, "Good"))
	if !got.IsValid() || got.Filename != "fixture.go" {
		t.Fatalf("ScopeKeyPos = %v, want a valid position in the fixture", got)
	}
	// Cross-check against FindScopeKey, which reaches the same method by a
	// different route.
	sk, err := FindScopeKey(fset, ptrTo(pkg, "Good"))
	if err != nil || sk == nil {
		t.Fatalf("FindScopeKey: %v", err)
	}
	if got != sk.Pos {
		t.Fatalf("ScopeKeyPos = %v, FindScopeKey said %v", got, sk.Pos)
	}

	if p := ScopeKeyPos(fset, ptrTo(pkg, "Plain")); p.IsValid() {
		t.Fatalf("ScopeKeyPos on a type with no such method = %v, want the zero position", p)
	}
	if p := ScopeKeyPos(fset, types.Typ[types.Int]); p.IsValid() {
		t.Fatalf("ScopeKeyPos on int = %v, want the zero position", p)
	}
}

// TestPromotedScopeKey covers the distinction the "no ScopeKey method"
// diagnostic needs to make: a type where `x.ScopeKey(ctx)` compiles but
// only through an embedded field, which servo deliberately will not use.
func TestPromotedScopeKey(t *testing.T) {
	pkg, _, _ := checkScopeKeyFixture(t)

	if !PromotedScopeKey(ptrTo(pkg, "Embedded")) {
		t.Fatal("an embedded ScopeKey was not reported as promoted")
	}
	// Reached through the value type too, not just the pointer.
	if !PromotedScopeKey(pkg.Scope().Lookup("Embedded").Type()) {
		t.Fatal("promotion not seen through the value type")
	}
	if PromotedScopeKey(ptrTo(pkg, "Good")) {
		t.Fatal("a directly declared ScopeKey was reported as promoted")
	}
	if PromotedScopeKey(ptrTo(pkg, "Plain")) {
		t.Fatal("a type with no ScopeKey at all was reported as promoted")
	}
}
