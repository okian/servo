package graph

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
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
