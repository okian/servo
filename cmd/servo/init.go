package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/okian/servo/v3/internal/load"
)

const specTemplate = `//go:build %s

package %s

//go:generate go run github.com/okian/servo/v3/cmd/servo generate%s

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		// servo.Root[*yourpkg.YourType](),
	)
}
`

// runInit scaffolds the spec file with the correct build tag, blank-line
// placement, and go:generate directive.
func runInit(dir string, tags []string) error {
	path := filepath.Join(dir, specFileName(tags))
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("servo init: %s already exists", path)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	constraint := load.BuildTag
	generateFlags := ""
	if len(tags) > 0 {
		constraint += " && " + strings.Join(tags, " && ")
		generateFlags = " --tags=" + strings.Join(tags, ",")
	}
	content := fmt.Sprintf(specTemplate, constraint, detectPackageName(dir), generateFlags)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("servo init: wrote %s\n", path)

	// Scaffolding a variant is exactly where the one mistake this feature
	// allows gets made: a sibling spec that does not exclude the new tags
	// produces two generated files that compile together, and `servo
	// generate` will refuse until it is fixed. Saying so here is cheaper
	// than saying so later.
	if len(tags) > 0 {
		warnAboutUnexcludedSiblings(dir, path, tags)
	}
	return nil
}

// specFileName keeps a variant's spec beside the default one rather than
// colliding with it: servo_spec.go, servo_spec_prod.go.
func specFileName(tags []string) string {
	if len(tags) == 0 {
		return "servo_spec.go"
	}
	return "servo_spec_" + strings.Join(tags, "_") + ".go"
}

// warnAboutUnexcludedSiblings names every existing spec file in dir whose
// own constraint is still satisfied when the new variant's tags are set.
func warnAboutUnexcludedSiblings(dir, ownPath string, tags []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	active := append([]string{load.BuildTag}, tags...)
	fset := token.NewFileSet()
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() || path == ownPath || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly|parser.ParseComments)
		if err != nil {
			continue
		}
		expr, ok := load.FileConstraint(f)
		if !ok || !load.FileRequiresBuildTag(f, load.BuildTag) {
			continue
		}
		if !load.ConstraintSatisfiedBy(expr, active) {
			continue
		}
		fmt.Printf("servo init: %s is also visible with --tags=%s, so both would generate a file and the two would compile together.\n", e.Name(), strings.Join(tags, ","))
		fmt.Printf("            Narrow it to `//go:build %s && !%s` (or otherwise exclude the new tags) before running servo generate.\n", load.BuildTag, strings.Join(tags, " && !"))
	}
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
