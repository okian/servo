package main

import (
	"fmt"
	"go/ast"
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

// mainTemplate is the main.go init writes beside a package main spec
// when nothing there declares func main yet: the documented one, so that
// `go generate ./... && go run .` works in the first minute instead of
// after the reader has gone looking for how a generated App is run.
const mainTemplate = `package main

import "github.com/okian/servo/v3/servo"

// servo.Main builds the App with New, runs it until the process is told
// to stop, shuts it down within a deadline, and exits non-zero when
// anything along the way failed.
func main() { servo.Main(New) }
`

const mainFileName = "main.go"

// runInit scaffolds the spec file with the correct build tag, blank-line
// placement, and go:generate directive — and, for a package main that
// has no main function yet, the main.go that runs what gets generated.
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

	// After the spec and directive files, so the check for an existing
	// func main reads the directory as it now stands.
	wroteMain := false
	if pkgName == "main" && !declaresMain(dir) {
		mainPath := filepath.Join(dir, mainFileName)
		if _, err := os.Stat(mainPath); os.IsNotExist(err) {
			if err := os.WriteFile(mainPath, []byte(mainTemplate), 0o644); err != nil {
				return err
			}
			fmt.Printf("servo init: wrote %s\n", mainPath)
			wroteMain = true
		}
	}

	runLine := ""
	if wroteMain {
		runLine = "    go run " + goRunTarget(dir) + "\n"
	}
	fmt.Printf(`
Next, once per module, so that go generate can run the generator and so
that every machine runs the same one:

    go get -tool %s/cmd/servo

Then declare your roots in %s and run:

    go generate ./...
%s`, path.Dir(graph.ServoPackagePath), specPath, runLine)

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

// declaresMain reports whether any non-test .go file in dir declares a
// top-level func main — whatever the file is called, since a second main
// beside it would not compile. A file that does not parse is skipped, as
// detectPackageName skips one: it is more likely being edited than
// holding the program's entry point.
func declaresMain(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
				return true
			}
		}
	}
	return false
}

// goRunTarget spells dir the way `go run` needs a relative directory
// spelled — with a leading ./, without which it is read as an import
// path.
func goRunTarget(dir string) string {
	clean := filepath.Clean(dir)
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return clean
	}
	return "./" + filepath.ToSlash(clean)
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
