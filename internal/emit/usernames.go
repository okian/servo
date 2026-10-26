package emit

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"sort"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/load"
)

// userDecls is what the injector package already declares outside its spec
// files: the names the generated file, which joins that package, must not
// declare again. The load never sees a previous run's output — every
// generated file is gated !servoinject and the load runs with the tag set
// — so this is the user's code and nothing of servo's.
type userDecls struct {
	// top is every package-level name, with where it is declared.
	top map[string]token.Position
	// methods is every method declared on a type named in recvs, by
	// receiver type name: a hand-written method on the generated App is
	// ordinary Go, and shares App's namespace with every field and method
	// the generator writes.
	methods map[string]map[string]token.Position
}

// collectUserDecls reads the declarations of pkg's files, skipping the
// ones only a servoinject build compiles — the spec and anything gated
// like it never share a build with a generated file. A package without
// syntax (one a test built by hand) falls back to its type-checked scope.
func collectUserDecls(pkg *packages.Package, recvs ...string) userDecls {
	d := userDecls{top: map[string]token.Position{}, methods: map[string]map[string]token.Position{}}
	if pkg == nil {
		return d
	}
	position := func(p token.Pos) token.Position {
		if pkg.Fset == nil {
			return token.Position{}
		}
		return pkg.Fset.Position(p)
	}
	if len(pkg.Syntax) == 0 {
		if pkg.Types != nil {
			for _, name := range pkg.Types.Scope().Names() {
				d.top[name] = position(pkg.Types.Scope().Lookup(name).Pos())
			}
		}
		return d
	}
	wanted := map[string]bool{}
	for _, r := range recvs {
		wanted[r] = true
	}
	add := func(id *ast.Ident) {
		if id.Name != "_" {
			d.top[id.Name] = position(id.Pos())
		}
	}
	for _, f := range pkg.Syntax {
		if load.FileRequiresBuildTag(f, load.BuildTag) {
			continue
		}
		for _, decl := range f.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil {
					if decl.Name.Name != "init" {
						add(decl.Name)
					}
					continue
				}
				if recv := receiverTypeName(decl.Recv); wanted[recv] {
					if d.methods[recv] == nil {
						d.methods[recv] = map[string]token.Position{}
					}
					d.methods[recv][decl.Name.Name] = position(decl.Name.Pos())
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						add(spec.Name)
					case *ast.ValueSpec:
						for _, id := range spec.Names {
							add(id)
						}
					}
				}
			}
		}
	}
	return d
}

// receiverTypeName is the name of the type a method is declared on,
// pointer or not, "" for anything it cannot name.
//
// Go lets the receiver type sit in parentheses, around the star or inside
// it — (App), *(App), (*App) — and the method is App's all the same, so
// they are unwrapped on both sides of the one star a receiver may have.
// Read only as plainly spelled, such a method escaped the collision check,
// and the generated file declared it a second time.
func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	expr := ast.Unparen(recv.List[0].Type)
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = ast.Unparen(star.X)
	}
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr: // a generic receiver: App[T]
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// names lists the package-level names, sorted, for the allocators.
func (d userDecls) names() []string {
	out := make([]string, 0, len(d.top))
	for name := range d.top {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// methodNames lists the methods declared on recv, sorted.
func (d userDecls) methodNames(recv string) []string {
	out := make([]string, 0, len(d.methods[recv]))
	for name := range d.methods[recv] {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// collisions reports the names the generated file could not step around,
// each at the user's declaration: the exported names the method set
// documents (App and its constructors, Values), App's public methods, and
// the packages the emitters write by their own names — context, os, the
// servo runtime and the rest of what they hard-code. Everything else the
// file declares was allocated clear of the user's names to begin with.
//
// These used to surface as compile errors in the generated file itself,
// which generate then blamed on servo.
func (e *emitter) collisions(user userDecls) error {
	pkg := e.pkgName
	var errs []error
	report := func(pos token.Position, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", pos, fmt.Sprintf(format, args...)))
	}
	fixed := []struct{ name, what string }{
		{e.appType(), "the type " + e.constructorName() + " returns"},
		{e.constructorName(), "the constructor"},
	}
	if e.takesValues() {
		fixed = append(fixed,
			struct{ name, what string }{e.valuesType(), "the struct of values the caller supplies"},
			struct{ name, what string }{e.withConstructorName(), "the constructor that takes them"})
	}
	if e.emitsValidateConfig() {
		fixed = append(fixed, struct{ name, what string }{"ValidateConfig", "the function that checks the configuration without building anything"})
	}
	for _, f := range fixed {
		if pos, ok := user.top[f.name]; ok {
			report(pos, "%s is declared in package %s, and the generated file declares %s, %s, under that name — rename yours, or delete it if it is left over from a hand-written %s the generated one replaces",
				f.name, pkg, f.name, f.what, f.name)
		}
	}
	methods := []string{"Run", "Shutdown", "Health", "Ready", "Graph", "Report"}
	if e.http != nil {
		methods = append(methods, "HTTPHandler")
	}
	for _, m := range methods {
		if pos, ok := user.methods[e.appType()][m]; ok {
			report(pos, "%s.%s is declared in package %s, and %s is part of the generated %s's method set — rename yours", e.appType(), m, pkg, m, e.appType())
		}
	}
	for path, ident := range e.imports.Imports() {
		if pos, ok := user.top[ident]; ok {
			report(pos, "%s is declared in package %s, and the generated file imports %s under that name — rename yours", ident, pkg, path)
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}
