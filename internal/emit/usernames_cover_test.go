package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestReceiverTypeNameReadsEveryReceiverSpelling: a hand-written method is
// matched to the generated App by its receiver's type name, read from
// syntax that need not type-check. A method on a generic type is declared
// with its type parameters, one or several, and is still a method of that
// type; so is one whose receiver type sits in parentheses, around the star
// or inside it, as Go allows. A receiver list with nothing in it, or a
// receiver no method can be declared on — a slice, a pointer to a
// pointer — names no type at all, as it names none to the compiler.
func TestReceiverTypeNameReadsEveryReceiverSpelling(t *testing.T) {
	for _, c := range []struct{ decl, want string }{
		{"func (a App) M() {}", "App"},
		{"func (a *App) M() {}", "App"},
		{"func (a App[T]) M() {}", "App"},
		{"func (a *App[T]) M() {}", "App"},
		{"func (a App[K, V]) M() {}", "App"},
		{"func (a *App[K, V]) M() {}", "App"},
		{"func (a (App)) M() {}", "App"},
		{"func (a *(App)) M() {}", "App"},
		{"func (a (*App)) M() {}", "App"},
		{"func (a ((*App))) M() {}", "App"},
		{"func (a (*(App))) M() {}", "App"},
		{"func (a *(App[T])) M() {}", "App"},
		{"func (a (*App[K, V])) M() {}", "App"},
		{"func () M() {}", ""},
		{"func (a []App) M() {}", ""},
		{"func (a **App) M() {}", ""},
		{"func (a *(*App)) M() {}", ""},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), "m.go", "package app\n"+c.decl, 0)
		if err != nil {
			t.Fatalf("%s: %v", c.decl, err)
		}
		if got := receiverTypeName(f.Decls[0].(*ast.FuncDecl).Recv); got != c.want {
			t.Errorf("%s: receiverTypeName = %q, want %q", c.decl, got, c.want)
		}
	}
}

// TestCollectUserDeclsWithoutAFileSet: a package a test built by hand can
// have neither syntax nor a file set. Its names still count against the
// generated file's; they just have no position to be reported at.
func TestCollectUserDeclsWithoutAFileSet(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", "package app\ntype App struct{}\nvar os = 1\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/app", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := collectUserDecls(&packages.Package{Name: "app", PkgPath: "example.com/app", Types: pkg})
	if got := d.names(); len(got) != 2 || got[0] != "App" || got[1] != "os" {
		t.Fatalf("names = %v, want [App os]", got)
	}
	if pos := d.top["App"]; pos != (token.Position{}) {
		t.Errorf("App is at %v without a file set to place it, want no position", pos)
	}
}
