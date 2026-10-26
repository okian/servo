package emit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// fixturePkg is one package of a test's fixture module: its import path
// and its one source file.
type fixturePkg struct{ path, src string }

// typecheckGenerated compiles a generated file beside the fixture packages
// it was generated from — the injector package as `go build` sees it once
// generate has written the file. The last package is the injector. Every
// other package either side imports is loaded for real, in one load so
// they share object identity, which makes the check follow whatever the
// emitters import rather than a list kept beside them.
//
// `servo generate` compiles what it writes too (load.Verify), but only
// for the graphs a real module declares; this reaches every shape a test
// can build, without a module on disk.
func typecheckGenerated(t *testing.T, generated []byte, pkgs ...fixturePkg) {
	t.Helper()
	fset := token.NewFileSet()
	own := map[string]bool{}
	for _, p := range pkgs {
		own[p.path] = true
	}
	seen := map[string]bool{}
	var external []string
	collect := func(f *ast.File) {
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("import path %s: %v", imp.Path.Value, err)
			}
			if !own[p] && !seen[p] {
				seen[p] = true
				external = append(external, p)
			}
		}
	}
	files := make([]*ast.File, len(pkgs))
	for i, p := range pkgs {
		f, err := parser.ParseFile(fset, path.Base(p.path)+".go", p.src, 0)
		if err != nil {
			t.Fatalf("parse fixture %s: %v", p.path, err)
		}
		files[i] = f
		collect(f)
	}
	gen, err := parser.ParseFile(fset, "servo_gen.go", generated, 0)
	if err != nil {
		t.Fatalf("parse generated file: %v\n---\n%s", err, generated)
	}
	collect(gen)
	sort.Strings(external)

	imp := newPkgImporter(loadPackages(t, external)...)
	for i, p := range pkgs {
		check := []*ast.File{files[i]}
		if i == len(pkgs)-1 {
			check = append(check, gen)
		}
		pkg, err := (&types.Config{Importer: imp}).Check(p.path, fset, check, nil)
		if err != nil {
			if i == len(pkgs)-1 {
				t.Fatalf("generated file does not compile beside its package: %v\n---\n%s", err, generated)
			}
			t.Fatalf("typecheck fixture %s: %v", p.path, err)
		}
		imp.byPath[p.path] = pkg
	}
}

// buildResolvedFromPkgs is buildResolvedFrom over a fixture of several
// packages, the last of which is the injector. Each is type-checked
// against the ones before it; roots are named by type, looked up in the
// injector package first and then in the others, in order.
func buildResolvedFromPkgs(t *testing.T, rootNames []string, pkgs ...fixturePkg) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	return buildFixture(t, fixtureSpec{roots: rootNames}, pkgs...)
}

// fixtureSpec is what a test's spec file would declare, by type name: a
// leading * makes a pointer to the named type. Roots are always pointers.
// Overrides are {interface, concrete} pairs, and declaring any resolves
// the override variant's graph, the one NewTestApp is generated from.
type fixtureSpec struct {
	roots     []string
	values    []string
	overrides [][2]string
	scopes    [][2]string // {implementation, accessor interface}
	// configs scans the packages for //servo:config types, as generate
	// does; configFile declares a servo.ConfigFile with that path.
	configs    bool
	configFile string
}

// buildFixture resolves pkgs (the last is the injector) under spec.
func buildFixture(t *testing.T, fs fixtureSpec, pkgs ...fixturePkg) (*resolve.Resolved, *load.Spec) {
	t.Helper()
	servoPkg := loadServoPackage(t)
	fset := token.NewFileSet()
	imp := newPkgImporter(servoPkg)
	loaded := make([]*packages.Package, len(pkgs))
	scope := map[string]bool{}
	mod := &packages.Module{Path: fixtureModulePath, Main: true}
	for i, p := range pkgs {
		f, err := parser.ParseFile(fset, path.Base(p.path)+".go", p.src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", p.path, err)
		}
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		pkg, err := (&types.Config{Importer: imp}).Check(p.path, fset, []*ast.File{f}, info)
		if err != nil {
			t.Fatalf("typecheck %s: %v", p.path, err)
		}
		imp.byPath[p.path] = pkg
		loaded[i] = &packages.Package{Name: pkg.Name(), PkgPath: p.path, Types: pkg, Fset: fset, Syntax: []*ast.File{f}, TypesInfo: info, Module: mod}
		scope[p.path] = true
	}
	injector := loaded[len(loaded)-1]
	var configs []*graph.ConfigDecl
	if fs.configs {
		var err error
		if configs, err = graph.ScanConfigs(loaded); err != nil {
			t.Fatalf("ScanConfigs: %v", err)
		}
	}
	lookup := func(name string) types.Type {
		ptr := strings.HasPrefix(name, "*")
		name = strings.TrimPrefix(name, "*")
		for i := len(loaded) - 1; i >= 0; i-- {
			if obj := loaded[i].Types.Scope().Lookup(name); obj != nil {
				if ptr {
					return types.NewPointer(obj.Type())
				}
				return obj.Type()
			}
		}
		t.Fatalf("no fixture package declares %s", name)
		return nil
	}
	spec := &load.Spec{InjectorPkg: injector}
	for i, name := range fs.roots {
		typ := lookup("*" + name)
		spec.Roots = append(spec.Roots, load.RootDecl{Key: graph.NewKey(typ, ""), Type: typ, Pos: token.Position{Filename: "spec.go", Line: 9 + i}})
	}
	for i, name := range fs.values {
		typ := lookup(name)
		spec.Values = append(spec.Values, load.ValueDecl{Key: graph.NewKey(typ, ""), Type: typ, Pos: token.Position{Filename: "spec.go", Line: 20 + i, Column: 3}})
	}
	for i, pair := range fs.overrides {
		iface, concrete := lookup(pair[0]), lookup(pair[1])
		spec.Overrides = append(spec.Overrides, load.BindDecl{
			Iface: graph.NewKey(iface, ""), IfaceType: iface,
			Concrete: graph.NewKey(concrete, ""), ConcreteType: concrete,
			Pos: token.Position{Filename: "spec.go", Line: 30 + i, Column: 3},
		})
	}
	for i, pair := range fs.scopes {
		impl, iface := lookup("*"+pair[0]), lookup(pair[1])
		spec.Scopes = append(spec.Scopes, load.ScopeDecl{
			Impl: graph.NewKey(impl, ""), ImplType: impl,
			Iface: graph.NewKey(iface, ""), IfaceType: iface,
			Pos: token.Position{Filename: "spec.go", Line: 40 + i},
		})
	}
	caps, err := graph.LoadCapabilities(servoPkg.Types)
	if err != nil {
		t.Fatalf("LoadCapabilities: %v", err)
	}
	candidates, _ := graph.ScanCandidates(loaded, injector.PkgPath)
	if fs.configFile != "" {
		spec.ConfigFile = &load.ConfigFileDecl{Path: fs.configFile, Pos: token.Position{Filename: "spec.go", Line: 50}}
	}
	resolved, diags := resolve.Resolve(resolve.Input{
		Spec: spec, Candidates: candidates, Caps: caps, Scope: scope, Fset: fset, Pkgs: loaded,
		ExtraBinds: spec.Overrides, Configs: configs,
	})
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	return resolved, spec
}

// fixtureModulePath is the module every fixture package path lives under
// when runGenerated writes it to disk: example.com/app is the directory
// app, and so on.
const fixtureModulePath = "example.com"

// runGenerated writes a throwaway module holding files — fixture sources
// and the test files that drive them, keyed by slash path relative to the
// module root — plus generated as servo_gen.go in injectorDir, and runs
// `go test` over it. It is the one way to see what generated code does
// rather than how it reads. The servo module is replaced by this
// checkout, so the file runs against the runtime it was generated for.
// Skipped under -short, since it builds a module.
func runGenerated(t *testing.T, injectorDir string, generated []byte, files map[string]string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs a module")
	}
	dir := t.TempDir()
	write := func(rel string, content []byte) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module "+fixtureModulePath+"\n\ngo 1.26.0\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+repoRootDir(t)+"\n"))
	for rel, content := range files {
		write(rel, []byte(content))
	}
	write(path.Join(injectorDir, "servo_gen.go"), generated)

	run := func(args ...string) string {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		// The fixture is its own module: neither a workspace the outer run
		// happens to sit in nor a toolchain download may reach into it.
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %v in the fixture module: %v\n%s\n--- generated ---\n%s", args, err, out, generated)
		}
		return string(out)
	}
	run("mod", "tidy")
	return run("test", "-count=1", "./...")
}

// repoRootDir is this checkout's module root, two directories above this
// file, so a fixture module can replace the servo module without a
// machine-specific path.
func repoRootDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}
