package emit

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runtimeImports are the only packages of servo's own module that generated
// code may import: the runtime it calls into. Everything else in the module
// (the generator, servotest, middleware) has to be deletable without the
// generated package noticing.
var runtimeImports = map[string]bool{
	"github.com/okian/servo/v3/servo":     true,
	"github.com/okian/servo/v3/servoimpl": true,
	"github.com/okian/servo/v3/conf":      true, // the generated config loaders' coercions
}

// TestConformance enforces the two promises the README's Layout section
// makes: neither the runtime packages nor any generated file imports
// reflect for servo's own wiring, and generated code imports nothing from
// servo's module but its runtime. Reflection inside encoding/json, which
// emitted HTTP servers use, is everyone's and is not counted.
func TestConformance(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "golden", "*.golden"))
	if err != nil || len(goldens) == 0 {
		t.Fatalf("no goldens found (err=%v)", err)
	}
	for _, path := range goldens {
		for _, imp := range importsOf(t, path) {
			if imp == "reflect" {
				t.Errorf("%s: generated code imports reflect", path)
			}
			if strings.HasPrefix(imp, "github.com/okian/servo/v3/") && !runtimeImports[imp] {
				t.Errorf("%s: generated code imports %s, which is not part of servo's runtime", path, imp)
			}
		}
	}

	for _, dir := range []string{"../../servo", "../../servoimpl", "../../conf"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no Go files in %s (err=%v)", dir, err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			for _, imp := range importsOf(t, path) {
				if imp == "reflect" {
					t.Errorf("%s: the runtime imports reflect", path)
				}
			}
		}
	}
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: bad import path %s", path, spec.Path.Value)
		}
		out = append(out, p)
	}
	return out
}
