package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/load"
)

const specTemplate = `//go:build %s

package %s

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		// servo.Root[*yourpkg.YourType](),
	)
}
`

// generateTemplate is a second, deliberately untagged file holding nothing
// but the go:generate directive.
//
// The directive used to live in the spec file, where it could never run:
// go generate honours build constraints, so a directive inside the
// //go:build servoinject file is invisible to `go generate ./...` — which
// exits 0, prints nothing, and generates nothing. Silence is the worst
// failure mode a tool whose whole claim is build-time checking can have.
//
// `go tool servo`, not `go run <path>`: a consumer requires servo for the
// marker package alone, so the generator's own dependencies are not in
// their build list and `go run github.com/okian/servo/v3/cmd/servo` fails
// on a missing go.sum entry. The tool directive puts the generator in
// go.mod, which also pins the version — the thing that decides whether a
// developer and CI produce the same file.
const generateTemplate = `package %s

//go:generate go tool servo generate%s
`

const generateFileName = "servo_generate.go"

// runInit scaffolds the spec file with the correct build tag, blank-line
// placement, and go:generate directive.
func runInit(dir string, tags []string) error {
	specPath := filepath.Join(dir, specFileName(tags))
	if _, err := os.Stat(specPath); err == nil {
		return fmt.Errorf("servo init: %s already exists", specPath)
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
	pkgName := detectPackageName(dir)
	content := fmt.Sprintf(specTemplate, constraint, pkgName)
	if err := os.WriteFile(specPath, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("servo init: wrote %s\n", specPath)

	// One directive file per directory, not per variant: `go generate` has
	// no build tags to select between them, so a second one would just be
	// a duplicate directive running the same generation twice.
	genPath := filepath.Join(dir, generateFileName)
	if _, err := os.Stat(genPath); err != nil {
		if err := os.WriteFile(genPath, []byte(fmt.Sprintf(generateTemplate, pkgName, generateFlags)), 0o644); err != nil {
			return err
		}
		fmt.Printf("servo init: wrote %s\n", genPath)
	}
	fmt.Printf(`
Next, once per module, so that go generate can run the generator and so
that every machine runs the same one:

    go get -tool %s/cmd/servo

Then declare your roots in %s and run:

    go generate ./...
`, path.Dir(graph.ServoPackagePath), specPath)

	// Scaffolding a variant is exactly where the one mistake this feature
	// allows gets made: a sibling spec that does not exclude the new tags
	// produces two generated files that compile together, and `servo
	// generate` will refuse until it is fixed. Saying so here is cheaper
	// than saying so later.
	if len(tags) > 0 {
		warnAboutUnexcludedSiblings(dir, specPath, tags)
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
