package graph

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestTypeStringUnaliasesThroughPointer covers a type alias to a defined
// type, referenced through a pointer: type DBAlias = DB means *DB and
// *DBAlias are the identical Go type (types.Identical agrees), but
// types.Unalias only resolves its own argument, not types nested inside
// it — a naive types.Unalias(*Pointer) leaves the pointer's element
// unresolved. A provider declared to return *DBAlias must produce the
// exact same Key as one returning *DB, or the two are wrongly treated as
// different dependencies.
func TestTypeStringUnaliasesThroughPointer(t *testing.T) {
	const src = `
package probe

type DB struct{}
type DBAlias = DB

func NewDB() *DB { return nil }
func NewAlias() *DBAlias { return nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/probe", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}

	resultOf := func(funcName string) types.Type {
		fn := pkg.Scope().Lookup(funcName).(*types.Func)
		return fn.Type().(*types.Signature).Results().At(0).Type()
	}
	dbResult, aliasResult := resultOf("NewDB"), resultOf("NewAlias")

	if !types.Identical(dbResult, aliasResult) {
		t.Fatalf("*DB and *DBAlias are not types.Identical (test assumption broken)")
	}
	want := "*example.com/probe.DB"
	if got := TypeString(dbResult); got != want {
		t.Errorf("TypeString(*DB) = %q, want %q", got, want)
	}
	if got := TypeString(aliasResult); got != want {
		t.Errorf("TypeString(*DBAlias) = %q, want %q (must match *DB's key exactly)", got, want)
	}
}

// TestTypeStringUnaliasesEveryPosition is the same rule one level down: an
// alias inside a type argument, or inside any other composite type, names
// the identical type its target does, so the two must produce one key.
// NewService(c *Cache[DBAlias]) used to report "no provider for
// *Cache[DBAlias]" beside a NewCache() *Cache[*DB] that types.Identical
// says is the very same type.
func TestTypeStringUnaliasesEveryPosition(t *testing.T) {
	const src = `
package probe

type DB struct{}
type DBAlias = *DB

type Cache[T any] struct{}
type Pair[K comparable, V any] struct{}
type Getter[T any] interface{ Get() T }

// A generic alias, instantiated, is the instantiated target.
type CacheOf[T any] = Cache[T]

type Embedded = DB

var (
	viaArg        *Cache[DBAlias]
	viaArgPlain   *Cache[*DB]
	genericAlias  *CacheOf[DBAlias]
	nestedAlias   *Cache[Pair[string, DBAlias]]
	nestedPlain   *Cache[Pair[string, *DB]]
	sliceAlias    []DBAlias
	slicePlain    []*DB
	arrayAlias    [2]DBAlias
	arrayPlain    [2]*DB
	mapAlias      map[string]DBAlias
	mapPlain      map[string]*DB
	chanAlias     <-chan DBAlias
	chanPlain     <-chan *DB
	funcAlias     func(DBAlias, ...DBAlias) (DBAlias, error)
	funcPlain     func(*DB, ...*DB) (*DB, error)
	namedParams   func(db DBAlias) (out DBAlias)
	namedPlain    func(db *DB) (out *DB)
	structAlias   struct{ D DBAlias ` + "`json:\"d\"`" + ` }
	structPlain   struct{ D *DB ` + "`json:\"d\"`" + ` }
	ifaceAlias    interface{ Get(DBAlias) DBAlias }
	ifacePlain    interface{ Get(*DB) *DB }
	embedAlias    interface{ Getter[DBAlias]; Close() }
	embedPlain    interface{ Getter[*DB]; Close() }
	noArgsAlias   func() DBAlias
	noArgsPlain   func() *DB
	embeddedAlias struct{ Embedded }
	embeddedPlain struct{ DB }
)
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/probe", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	typeOf := func(name string) types.Type { return pkg.Scope().Lookup(name).Type() }

	for _, pair := range [][2]string{
		{"viaArg", "viaArgPlain"},
		{"genericAlias", "viaArgPlain"},
		{"nestedAlias", "nestedPlain"},
		{"sliceAlias", "slicePlain"},
		{"arrayAlias", "arrayPlain"},
		{"mapAlias", "mapPlain"},
		{"chanAlias", "chanPlain"},
		{"funcAlias", "funcPlain"},
		{"namedParams", "namedPlain"},
		{"structAlias", "structPlain"},
		{"ifaceAlias", "ifacePlain"},
		{"embedAlias", "embedPlain"},
		{"noArgsAlias", "noArgsPlain"},
	} {
		aliased, plain := typeOf(pair[0]), typeOf(pair[1])
		if !types.Identical(aliased, plain) {
			t.Fatalf("%s and %s are not types.Identical (test assumption broken)", pair[0], pair[1])
		}
		if got, want := TypeString(aliased), TypeString(plain); got != want {
			t.Errorf("TypeString(%s) = %q, want %q — the key of the identical %s", pair[0], got, want, pair[1])
		}
		if strings.Contains(TypeString(aliased), "Alias") || strings.Contains(TypeString(aliased), "CacheOf") {
			t.Errorf("TypeString(%s) = %q still names the alias", pair[0], TypeString(aliased))
		}
	}

	// An embedded field is named after the type written for it, so these
	// two are different types and must keep different keys.
	if types.Identical(typeOf("embeddedAlias"), typeOf("embeddedPlain")) {
		t.Fatal("struct{ Embedded } and struct{ DB } are types.Identical (test assumption broken)")
	}
	if TypeString(typeOf("embeddedAlias")) == TypeString(typeOf("embeddedPlain")) {
		t.Errorf("struct{ Embedded } and struct{ DB } collapsed to one key, %q", TypeString(typeOf("embeddedAlias")))
	}

	// A type with no alias anywhere in it is returned as the same object,
	// so rendering an ordinary key rebuilds nothing.
	plain := typeOf("nestedPlain")
	if unaliasDeep(plain) != plain {
		t.Error("unaliasDeep rebuilt a type that holds no alias")
	}
}

func TestKeyString(t *testing.T) {
	cases := []struct {
		key  Key
		want string
	}{
		{Key{Type: "example.com/store.Store"}, "example.com/store.Store"},
		{Key{Type: "example.com/store.Store", Tag: "primary"}, "example.com/store.Store#primary"},
	}
	for _, c := range cases {
		if got := c.key.String(); got != c.want {
			t.Errorf("Key%+v.String() = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestTypeStringSpellsTheEmptyInterfaceAny pins the one canonical spelling
// of the empty interface: written as any or as interface{}, at the top or
// as a type argument, it renders as "any", so both spellings share a key
// and names keep the form Go code writes.
func TestTypeStringSpellsTheEmptyInterfaceAny(t *testing.T) {
	const src = `
package probe

type Repo[T any] struct{}

var (
	argAny   *Repo[any]
	argEmpty *Repo[interface{}]
	topAny   any
	topEmpty interface{}
	inMap    map[string]interface{}
)
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/probe", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	typeOf := func(name string) string { return TypeString(pkg.Scope().Lookup(name).Type()) }
	for name, want := range map[string]string{
		"argAny":   "*example.com/probe.Repo[any]",
		"argEmpty": "*example.com/probe.Repo[any]",
		"topAny":   "any",
		"topEmpty": "any",
		"inMap":    "map[string]any",
	} {
		if got := typeOf(name); got != want {
			t.Errorf("TypeString(%s) = %q, want %q", name, got, want)
		}
	}
}

// TypeStringWith resolves aliases at every depth exactly as TypeString
// does; only the qualifier is the caller's. A diagnostic that spells a type
// the way a spec file writes it must still name the type its key names.
func TestTypeStringWithResolvesAliasesUnderTheCallersQualifier(t *testing.T) {
	const src = `
package probe

type DB struct{}
type DBAlias = *DB

type Cache[T any] struct{}

var viaArg *Cache[DBAlias]
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg, err := (&types.Config{Importer: importer.Default()}).Check("example.com/probe", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("typecheck: %v", err)
	}
	byName := func(p *types.Package) string { return p.Name() }
	if got, want := TypeStringWith(pkg.Scope().Lookup("viaArg").Type(), byName), "*probe.Cache[*probe.DB]"; got != want {
		t.Errorf("TypeStringWith(*Cache[DBAlias], by name) = %q, want %q", got, want)
	}
}
