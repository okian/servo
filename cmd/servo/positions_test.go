package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateNamesProvidersOutsideTheModuleByImportPath pins the one
// property of positions that only shows up with a provider from another
// module: the committed file must not carry the path this checkout keeps
// the dependency at. examples/http and the tutorial both pull middleware
// from the servo module itself — through a replace directive here, from
// the module cache for everyone else — and `servo check` on any other
// machine failed on exactly those lines until the position was written as
// the package's import path instead.
func TestGenerateNamesProvidersOutsideTheModuleByImportPath(t *testing.T) {
	dir := t.TempDir()
	root := repoRoot(t)
	write := func(rel, content string) { mustWriteFile(t, dir, rel, content) }

	write("go.mod", `module example.com/depprovider

go 1.23

require github.com/okian/servo/v3 v3.0.0

replace github.com/okian/servo/v3 => `+root+`
`)
	write("cmd/app/spec.go", `//go:build servoinject

package main

import (
	"github.com/okian/servo/v3/middleware"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*middleware.Recover](),
	)
}
`)
	runGoModTidy(t, dir)

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	gen, err := os.ReadFile(filepath.Join(dir, "cmd", "app", "servo_gen.go"))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	if strings.Contains(string(gen), root) {
		t.Errorf("generated file embeds this checkout's path %s:\n%s", root, gen)
	}
	const want = "github.com/okian/servo/v3/middleware/recover.go:"
	if !strings.Contains(string(gen), want) {
		t.Errorf("expected the provider to be named by its import path %q, got:\n%s", want, gen)
	}
	// The same rule is what lets `servo check` agree with a file generated
	// on another machine; here it at least has to agree with its own output.
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check after generate: %v", err)
	}
}
