package graph

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"
)

// mustCheckConfigPkg is mustCheck with everything ScanConfigs additionally
// needs: the parsed syntax (directives live in doc comments), Defs (the
// type behind a TypeSpec), and a main-module marker (packages outside the
// main module are never scanned, since nothing may write into the module
// cache).
func mustCheckConfigPkg(t *testing.T, importPath, filename, src string) *packages.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check(importPath, fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("typecheck %s: %v", filename, err)
	}
	return &packages.Package{
		Name: pkg.Name(), PkgPath: importPath, Types: pkg, Fset: fset,
		Syntax: []*ast.File{f}, TypesInfo: info,
		Module: &packages.Module{Main: true, Dir: "/mod"},
	}
}

func scanOne(t *testing.T, src string) ([]*ConfigDecl, error) {
	t.Helper()
	pkg := mustCheckConfigPkg(t, "example.com/db", "db/db.go", src)
	return ScanConfigs([]*packages.Package{pkg})
}

func TestScanConfigsParsesDirectiveAndTags(t *testing.T) {
	decls, err := scanOne(t, `
package db

import "time"

//servo:config prefix=POSTGRES
type dbConfig struct {
	dsn          string        `+"`config:\"dsn,required\"`"+`
	maxConns     int32         `+"`config:\"max_conns,default=10\"`"+`
	connLifetime time.Duration `+"`config:\"conn_lifetime,default=30m\"`"+`
	password     string        `+"`config:\"password,required,secret\"`"+`
	verbose      bool          `+"`config:\"verbose\"`"+`
	derived      string        // untagged: not loaded
}
`)
	if err != nil {
		t.Fatalf("ScanConfigs: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("got %d decls, want 1", len(decls))
	}
	d := decls[0]
	if d.TypeName != "dbConfig" || d.Prefix != "POSTGRES" || d.Section != "postgres" {
		t.Fatalf("decl = %s prefix=%s section=%s", d.TypeName, d.Prefix, d.Section)
	}
	if d.Key.Type != "example.com/db.dbConfig" {
		t.Fatalf("key = %s", d.Key.Type)
	}
	if len(d.Fields) != 5 {
		t.Fatalf("got %d fields, want 5 (untagged field must be skipped)", len(d.Fields))
	}

	byName := map[string]ConfigField{}
	for _, f := range d.Fields {
		byName[f.Name] = f
	}
	if f := byName["dsn"]; !f.Required || f.EnvName != "POSTGRES_DSN" || f.Kind != KindString {
		t.Errorf("dsn = %+v", f)
	}
	if f := byName["max_conns"]; f.EnvName != "POSTGRES_MAX_CONNS" || f.Kind != KindInt32 || !f.HasDefault || f.Default != "10" {
		t.Errorf("max_conns = %+v", f)
	}
	if f := byName["conn_lifetime"]; f.Kind != KindDuration || f.DefaultDuration != 30*time.Minute {
		t.Errorf("conn_lifetime = %+v", f)
	}
	if f := byName["password"]; !f.Secret || !f.Required {
		t.Errorf("password = %+v", f)
	}
	if f := byName["verbose"]; f.Kind != KindBool || f.Required || f.HasDefault {
		t.Errorf("verbose = %+v", f)
	}
}

func TestScanConfigsSectionKeyOverride(t *testing.T) {
	decls, err := scanOne(t, `
package db

//servo:config prefix=POSTGRES key=db
type cfg struct {
	dsn string `+"`config:\"dsn\"`"+`
}
`)
	if err != nil {
		t.Fatalf("ScanConfigs: %v", err)
	}
	if decls[0].Section != "db" {
		t.Fatalf("section = %q, want db", decls[0].Section)
	}
}

func TestScanConfigsSkipsNonMainModule(t *testing.T) {
	pkg := mustCheckConfigPkg(t, "example.com/dep", "dep/dep.go", `
package dep

//servo:config prefix=DEP
type cfg struct {
	x string `+"`config:\"x\"`"+`
}
`)
	pkg.Module = &packages.Module{Main: false}
	decls, err := ScanConfigs([]*packages.Package{pkg})
	if err != nil {
		t.Fatalf("ScanConfigs: %v", err)
	}
	if len(decls) != 0 {
		t.Fatalf("got %d decls from a non-main package, want 0", len(decls))
	}
}

func TestScanConfigsErrors(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"missing prefix", `
package db

//servo:config
type cfg struct {
	x string ` + "`config:\"x\"`" + `
}
`, "needs prefix="},
		{"lowercase prefix", `
package db

//servo:config prefix=postgres
type cfg struct {
	x string ` + "`config:\"x\"`" + `
}
`, "must be UPPER_SNAKE"},
		{"unknown option", `
package db

//servo:config prefix=DB mode=strict
type cfg struct {
	x string ` + "`config:\"x\"`" + `
}
`, `no option "mode"`},
		{"unknown directive", `
package db

//servo:confg prefix=DB
type cfg struct {
	x string ` + "`config:\"x\"`" + `
}
`, "unrecognized servo directive"},
		{"not a struct", `
package db

//servo:config prefix=DB
type cfg int
`, "not a struct"},
		{"no tagged fields", `
package db

//servo:config prefix=DB
type cfg struct {
	x string
}
`, "no `config:\"...\"` tagged fields"},
		{"bad tag name", `
package db

//servo:config prefix=DB
type cfg struct {
	x string ` + "`config:\"MaxConns\"`" + `
}
`, "must be lower_snake"},
		{"unknown tag option", `
package db

//servo:config prefix=DB
type cfg struct {
	x string ` + "`config:\"x,optional\"`" + `
}
`, `unknown option "optional"`},
		{"required with default", `
package db

//servo:config prefix=DB
type cfg struct {
	x string ` + "`config:\"x,required,default=y\"`" + `
}
`, "both required and has a default"},
		{"bad default", `
package db

//servo:config prefix=DB
type cfg struct {
	x int ` + "`config:\"x,default=ten\"`" + `
}
`, "not a valid int"},
		{"unsupported type", `
package db

//servo:config prefix=DB
type cfg struct {
	x []string ` + "`config:\"x\"`" + `
}
`, "unsupported type"},
		{"defined type", `
package db

type Port int

//servo:config prefix=DB
type cfg struct {
	x Port ` + "`config:\"x\"`" + `
}
`, "unsupported type"},
		{"nested struct", `
package db

type inner struct{}

//servo:config prefix=DB
type cfg struct {
	x inner ` + "`config:\"x\"`" + `
}
`, "unsupported type"},
		{"embedded field", `
package db

type inner struct{}

//servo:config prefix=DB
type cfg struct {
	inner ` + "`config:\"x\"`" + `
}
`, "embedded fields"},
		{"duplicate name", `
package db

//servo:config prefix=DB
type cfg struct {
	a string ` + "`config:\"x\"`" + `
	b string ` + "`config:\"x\"`" + `
}
`, `declared twice`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scanOne(t, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestScanConfigsRefusesTwoPerPackage(t *testing.T) {
	_, err := scanOne(t, `
package db

//servo:config prefix=A
type a struct {
	x string `+"`config:\"x\"`"+`
}

//servo:config prefix=B
type b struct {
	y string `+"`config:\"y\"`"+`
}
`)
	if err == nil || !strings.Contains(err.Error(), "second //servo:config type in package") {
		t.Fatalf("got err=%v, want the one-per-package refusal", err)
	}
}

func TestDirectiveLine(t *testing.T) {
	cases := []struct {
		comment, name, rest string
		ok                  bool
	}{
		{"//servo:config prefix=DB", "config", "prefix=DB", true},
		{"//servo:config", "config", "", true},
		{"//servo:confg x", "confg", "x", true},
		{"// servo:config prefix=DB", "", "", false}, // a space makes it prose, matching go:generate's rule
		{"// an ordinary comment", "", "", false},
		{"//servo:", "", "", false},
	}
	for _, tc := range cases {
		name, rest, ok := DirectiveLine(tc.comment)
		if name != tc.name || rest != tc.rest || ok != tc.ok {
			t.Errorf("DirectiveLine(%q) = %q, %q, %v; want %q, %q, %v", tc.comment, name, rest, ok, tc.name, tc.rest, tc.ok)
		}
	}
}
