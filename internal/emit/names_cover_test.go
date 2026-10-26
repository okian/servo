package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// TestBaseNameSpellsEveryTypeArgumentKind: a generic node's identifier
// carries its type arguments, so Repo[*User] and Repo[[]User] get names a
// reader can tell apart rather than repo and repo2. Every kind of argument
// has a spelling of its own, a pointer's star is dropped, and a type with
// nothing to spell, a struct literal, is T.
func TestBaseNameSpellsEveryTypeArgumentKind(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", "package app\ntype User struct{}\ntype Repo[T any] struct{}\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	user, repo := pkg.Scope().Lookup("User").Type(), pkg.Scope().Lookup("Repo").Type()
	str := types.Typ[types.String]

	for _, c := range []struct {
		arg  types.Type
		want string
	}{
		{types.NewPointer(user), "repoUser"},
		{str, "repoString"},
		{types.NewSlice(user), "repoUsers"},
		{types.NewArray(user, 4), "repoUsers"},
		{types.NewMap(str, user), "repoStringUserMap"},
		{types.NewChan(types.SendRecv, user), "repoUserChan"},
		{types.NewStruct(nil, nil), "repoT"},
	} {
		inst, err := types.Instantiate(nil, repo, []types.Type{c.arg}, true)
		if err != nil {
			t.Fatalf("Repo[%s]: %v", c.arg, err)
		}
		if got := baseName(types.NewPointer(inst)); got != c.want {
			t.Errorf("baseName(*Repo[%s]) = %q, want %q", c.arg, got, c.want)
		}
	}
}

// TestQualifiedBaseNameNeedsAPackage: qualifying by package is how two
// same-named nodes are told apart, but a type literal has no package to
// add, and neither has a predeclared type; each keeps its plain base name
// rather than gaining an empty or a made-up prefix.
func TestQualifiedBaseNameNeedsAPackage(t *testing.T) {
	iface := types.NewInterfaceType([]*types.Func{
		types.NewFunc(token.NoPos, nil, "Get", types.NewSignatureType(nil, nil, nil, nil, nil, false)),
	}, nil).Complete()
	for _, c := range []struct {
		name string
		t    types.Type
		want string
	}{
		{"a named type", namedType("Config"), "appConfig"},
		{"an interface literal", iface, "v"},
		{"a predeclared type", types.Universe.Lookup("error").Type(), "error"},
	} {
		if got := qualifiedBaseName(c.t); got != c.want {
			t.Errorf("%s: qualifiedBaseName = %q, want %q", c.name, got, c.want)
		}
	}
}
