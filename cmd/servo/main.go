// Command servo is the build-time dependency and lifecycle generator's
// CLI: resolve, diagnose, emit, and inspect.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okian/servo/v3/internal/load"
)

// registerBuildFlags adds the go command build flags servo passes through
// to the loader — same names, same spelling, same meaning as `go build`.
// Only the commands that actually load packages get them: `init`, `new`
// and `migrate` never call packages.Load, and `migrate` in particular
// walks the tree with go/parser without evaluating build constraints at
// all, so a -tags there would be a lie.
//
// -tags is not seeded from GOFLAGS, which is a deliberate divergence from
// the go command and the one place "similar to go itself" has to yield: the
// go command builds, servo writes a file you commit. See
// load.BuildFlags.Tags.
func registerBuildFlags(fs *flag.FlagSet) *load.BuildFlags {
	var b load.BuildFlags
	fs.StringVar(&b.Tags, "tags", "",
		"a comma-separated list of additional build tags to consider satisfied during the load")
	fs.StringVar(&b.Mod, "mod", "",
		"module download mode to use: readonly, vendor or mod; defaults to the go command's own choice")
	fs.Var(pathFlag{&b.ModFile}, "modfile",
		"read an alternate go.mod file instead of the one in the module root directory")
	fs.Var(pathFlag{&b.Overlay}, "overlay",
		"read a JSON config file that provides an overlay for build operations")
	return &b
}

// pathFlag resolves a path flag against the caller\'s working directory at
// parse time. The loader runs the go command with its Dir set to --dir, so
// a relative -modfile would otherwise be looked up there — meaning
// `servo check --dir cmd/app --overlay=overlay.json` would fail on an
// overlay.json sitting right next to the user, which is not how the same
// flag behaves on `go build`.
type pathFlag struct{ target *string }

func (f pathFlag) String() string {
	if f.target == nil {
		return ""
	}
	return *f.target
}

func (f pathFlag) Set(v string) error {
	if v == "" {
		*f.target = ""
		return nil
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return err
	}
	*f.target = abs
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "generate"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "generate":
		fs := flag.NewFlagSet("generate", flag.ContinueOnError)
		dir := fs.String("dir", ".", "directory to scan from; every injector found within it is generated")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runGenerate(load.Config{Dir: *dir, Build: *build})

	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		dir := fs.String("dir", ".", "directory to scan from; every injector found within it is checked")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runCheck(load.Config{Dir: *dir, Build: *build})

	case "graph":
		fs := flag.NewFlagSet("graph", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		format := fs.String("format", "text", "text|json|dot|mermaid")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runGraph(load.Config{Dir: *dir, Build: *build}, *format)

	case "explain":
		fs := flag.NewFlagSet("explain", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: servo explain [--json] <type>")
		}
		return runExplain(load.Config{Dir: *dir, Build: *build}, fs.Arg(0), *jsonOut)

	case "why":
		fs := flag.NewFlagSet("why", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: servo why [--json] <type>")
		}
		return runWhy(load.Config{Dir: *dir, Build: *build}, fs.Arg(0), *jsonOut)

	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		rejected := fs.Bool("rejected", false, "list excluded functions and why")
		all := fs.Bool("all", false, "include stdlib/third-party, not just the main module")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runList(load.Config{Dir: *dir, Build: *build}, *rejected, *all, *jsonOut)

	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		dir := fs.String("dir", ".", "directory to scaffold the spec file in")
		tags := fs.String("tags", "", "scaffold a variant spec gated on these build tags instead of the default one")
		if err := fs.Parse(args); err != nil {
			return err
		}
		build := load.BuildFlags{Tags: *tags}
		if err := build.Validate(); err != nil {
			return err
		}
		return runInit(*dir, build.TagList())

	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		build := registerBuildFlags(fs)
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runDoctor(load.Config{Dir: *dir, Build: *build})

	case "migrate":
		fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runMigrate(*dir)

	case "new":
		if len(args) < 1 {
			return fmt.Errorf("usage: servo new component <name> | servo new adapter <pkg> | servo new mock-adapter <moq|mockery|gomock> <GeneratedTypeName>")
		}
		return runNew(args[0], args[1:])

	default:
		return fmt.Errorf("servo: unknown command %q", cmd)
	}
}
