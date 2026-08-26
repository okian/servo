package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

const specTemplate = `//go:build servoinject

package %s

//go:generate go run github.com/okian/servo/v2/cmd/servo generate

import "github.com/okian/servo/v2/servo"

func wire() {
	servo.Build(
		// servo.Root[*yourpkg.YourType](),
	)
}
`

// runInit scaffolds the spec file with the correct build tag, blank-line
// placement, and go:generate directive.
func runInit(dir string) error {
	path := filepath.Join(dir, "servo_spec.go")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("servo init: %s already exists", path)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf(specTemplate, detectPackageName(dir))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("servo init: wrote %s\n", path)
	return nil
}

// detectPackageName peeks at any existing .go file in dir for its package
// name, falling back to "main" — the common case of a spec file living
// alongside a cmd/*/main.go.
func detectPackageName(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "main"
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
		if err != nil {
			continue
		}
		return f.Name.Name
	}
	return "main"
}
