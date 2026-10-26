// Command servo is the build-time dependency and lifecycle generator's
// CLI: resolve, diagnose, emit, and inspect.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/okian/servo/v3/internal/load"
)

// command is one servo subcommand: what help says about it, and how its
// arguments are parsed and run.
type command struct {
	name    string
	summary string
	// forms are the positional-argument synopses, one per shape the
	// command accepts — "<type>" for explain, or new's three kinds. None
	// for a command that takes no arguments.
	forms []string
	// formsNameFlags marks forms that spell out their own flags, for a
	// command whose flags apply to some forms and not others; its usage
	// lines are then the forms alone.
	formsNameFlags bool
	// minArgs and maxArgs bound the positional arguments. A negative
	// maxArgs leaves the upper bound to the command itself, which knows
	// which form it was given.
	minArgs, maxArgs int
	// setup registers the command's flags on fs and returns what runs it
	// once they are parsed. Dispatch, help and the usage line each call it
	// on a flag set of their own, so a flag registered here is in all
	// three. help itself has no setup: it is answered before any command
	// is looked up.
	setup func(fs *flag.FlagSet) func(args []string) error
}

// commands is every subcommand, in the order the reference documents
// them: the two you run in CI first, then the five that answer a question
// about one graph, then the ones that write or diagnose.
//
// It is the one source for dispatch, for `servo help`, for the usage line
// a mistake prints, and for the tests over all three. Those used to be
// written down separately and drifted apart: help advertised `servo
// explain <type> [--dir] [--json]` to a parser that refused that order,
// the usage error it printed instead never mentioned --dir, and the test
// claiming to cover every subcommand had quietly missed one. A synopsis
// generated from the flag set the parser itself uses cannot disagree with
// it.
var commands = []*command{
	{
		name:    "generate",
		summary: "resolve every injector found under --dir and write its generated file",
		setup:   setupGenerate,
	},
	{
		name:    "check",
		summary: "verify every generated file matches a fresh generation; writes nothing",
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from; every injector found within it is checked")
			return func([]string) error { return runCheckWith(cfg(), checkOptions{invoked: invocationOf(fs)}) }
		},
	},
	{
		name:    "graph",
		summary: "export one injector's resolved graph",
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from")
			format := fs.String("format", "text", "the output format, one of `text|json|dot|mermaid`")
			return func([]string) error { return runGraph(cfg(), *format) }
		},
	},
	{
		name:    "explain",
		summary: "which provider was selected for a type or an interface, and why",
		forms:   []string{"<type>"},
		minArgs: 1, maxArgs: 1,
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from")
			jsonOut := fs.Bool("json", false, "machine-readable output")
			return func(args []string) error { return runExplain(cfg(), args[0], *jsonOut) }
		},
	},
	{
		name:    "why",
		summary: "the shortest path from a root to that node; --all lists every path",
		forms:   []string{"<type>"},
		minArgs: 1, maxArgs: 1,
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from")
			jsonOut := fs.Bool("json", false, "machine-readable output")
			all := fs.Bool("all", false, "every path from a root to the node, not just the shortest")
			return func(args []string) error { return runWhyPaths(cfg(), args[0], *jsonOut, *all) }
		},
	},
	{
		name:    "list",
		summary: "the candidate index, or every excluded function and the rule that excluded it",
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from")
			rejected := fs.Bool("rejected", false, "list excluded functions and why")
			all := fs.Bool("all", false, "include stdlib/third-party, not just the main module")
			jsonOut := fs.Bool("json", false, "machine-readable output")
			return func([]string) error { return runList(cfg(), *rejected, *all, *jsonOut) }
		},
	},
	{
		name:    "config",
		summary: "every setting one injector's //servo:config types read: env name, file key, type, default",
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from")
			jsonOut := fs.Bool("json", false, "machine-readable output")
			return func([]string) error { return runConfig(cfg(), *jsonOut) }
		},
	},
	{
		name:    "init",
		summary: "scaffold a spec file, its go:generate file, and a main.go if there is none",
		setup: func(fs *flag.FlagSet) func([]string) error {
			dir := fs.String("dir", ".", "`path` to scaffold the spec file in")
			tags := fs.String("tags", "", "scaffold a variant spec gated on a comma-separated `tag,list` instead of the default one")
			return func([]string) error {
				build := load.BuildFlags{Tags: *tags}
				if err := build.Validate(); err != nil {
					return err
				}
				return runInit(*dir, build.TagList())
			}
		},
	},
	{
		name:    "doctor",
		summary: "diagnose setup problems before go generate ever runs",
		setup: func(fs *flag.FlagSet) func([]string) error {
			cfg := registerLoadFlags(fs, "`path` to scan from; every injector found within it is diagnosed")
			return func([]string) error { return runDoctorWith(cfg(), invocationOf(fs)) }
		},
	},
	{
		name:    "migrate",
		summary: "read v1 Register(X{}, N) calls and emit a v3 skeleton",
		setup: func(fs *flag.FlagSet) func([]string) error {
			dir := fs.String("dir", ".", "`path` to read v1 code from")
			return func([]string) error { return runMigrate(*dir) }
		},
	},
	{
		name:           "new",
		summary:        "print a complete file: a component, a third-party wrapper, or a mock adapter",
		forms:          []string{newComponentForm, newAdapterForm, newMockAdapterForm},
		formsNameFlags: true,
		minArgs:        1, maxArgs: -1,
		setup: func(fs *flag.FlagSet) func([]string) error {
			pkg := fs.String("package", "", "the `name` in the scaffold's package clause; component defaults to the type name lowercased, mock-adapter to "+strconv.Quote(defaultMockPackage))
			return func(args []string) error { return runNew(args[0], args[1:], *pkg) }
		},
	},
	{
		name:    "completion",
		summary: "print a completion script for bash, zsh or fish",
		forms:   []string{"<bash|zsh|fish>"},
		minArgs: 1, maxArgs: 1,
		// setup is attached in completion.go's init: the script is written
		// from this table, and an entry whose initializer reads the table
		// would be an initialization cycle.
	},
	{
		name:    "version",
		summary: "the version of servo that built this binary",
		setup: func(*flag.FlagSet) func([]string) error {
			return func([]string) error { return printVersion(os.Stdout) }
		},
	},
	{
		name:    "help",
		summary: "this list, or one command's usage",
		forms:   []string{"[command]"},
	},
}

// setupGenerate is generate's entry in the table, named because doctor
// reads a //go:generate line's arguments with exactly the flags generate
// takes — and a function the table's initializer reaches cannot read the
// table itself.
func setupGenerate(fs *flag.FlagSet) func([]string) error {
	cfg := registerLoadFlags(fs, "`path` to scan from; every injector found within it is generated")
	verbose := fs.Bool("v", false, "list every generated file: written, unchanged or removed")
	return func([]string) error {
		return runGenerateWith(cfg(), generateOptions{verbose: *verbose, invoked: invocationOf(fs)})
	}
}

// lookupCommand finds a command by name, nil when there is none.
func lookupCommand(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

// newFlagSet is the flag set every command parses with. Its output is
// discarded because servo reports a parse failure itself, once, with the
// command's own usage line: left to the flag package, the same error
// reached stderr twice with a dump of every flag in between, and `-h`
// came back as the error "flag: help requested" with exit status 1.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// flagSet is c's flags registered on a fresh set and nothing run — what
// help and the synopsis read.
func (c *command) flagSet() *flag.FlagSet {
	fs := newFlagSet(c.name)
	if c.setup != nil {
		c.setup(fs)
	}
	return fs
}

// run parses args for c and runs it. -h and --help are a request for c's
// help, answered on stdout with exit status 0 like `servo help <command>`,
// rather than the flag package's error.
func (c *command) run(args []string) error {
	fs := newFlagSet(c.name)
	action := c.setup(fs)
	positional, err := parseFlags(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return c.writeHelp(os.Stdout)
	case err != nil:
		return fmt.Errorf("servo %s: %w\n%s", c.name, err, c.usage())
	}

	// A command that takes no arguments has always ignored them, and an
	// existing `servo generate ./cmd/app` in somebody's script depends on
	// that exit status — wire takes package patterns, so it is exactly what
	// a wire user types. So it still ignores them, but out loud: the run
	// covers every injector under --dir, not the one the argument named.
	if c.maxArgs == 0 && len(positional) > 0 {
		fmt.Fprintln(os.Stderr, ignoredArgsWarning(c, fs, positional))
		positional = nil
	}
	if len(positional) < c.minArgs || (c.maxArgs >= 0 && len(positional) > c.maxArgs) {
		return errors.New(c.usage())
	}
	return action(positional)
}

// ignoredArgsWarning says which arguments a command that takes none is
// ignoring, and — for a command that reads a directory — what the
// argument was probably meant to be: a --dir.
func ignoredArgsWarning(c *command, fs *flag.FlagSet, positional []string) string {
	quoted := make([]string, len(positional))
	for i, a := range positional {
		quoted[i] = strconv.Quote(a)
	}
	msg := fmt.Sprintf("servo %s: ignoring %s — %s takes no arguments", c.name, strings.Join(quoted, " "), c.name)
	dir := fs.Lookup("dir")
	if dir == nil {
		return msg
	}
	msg += fmt.Sprintf(" and reads every injector under --dir (%s)", strconv.Quote(dir.Value.String()))
	if len(positional) != 1 {
		return msg + "; pass --dir to choose the directory"
	}
	// `./...` is the go command's spelling of what --dir already does.
	target := strings.TrimSuffix(strings.TrimSuffix(positional[0], "..."), "/")
	if target == "" || target == "." {
		return msg + ", which already covers everything below it"
	}
	return msg + "; to narrow it, pass --dir " + target
}

// parseFlags parses fs over args wherever its flags sit among the
// positional arguments, which it returns in order. The flag package stops
// at the first argument that is not a flag, so on its own it made `servo
// explain api.Server --json` a usage error and `servo check ./...
// --tags=prod` a silent check of the default configuration. Parsing again
// after each positional argument lets either order mean the same thing,
// and a "--" still ends the flags.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		// Parse consumes a "--" terminator and stops right after it.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// registerLoadFlags adds the flags every command that loads packages
// shares — --dir, --scope, and the go build flags — and returns the
// load.Config they describe, to be read once the flag set is parsed. It is
// the one place those flags are registered, so a flag every loading
// command gains is one change here rather than eight.
//
// --scope decides which modules are "the main module" when a go.work
// lists several. The default is the module --dir is in, alone: that is
// the module CI builds with GOWORK=off, and generating against any other
// set writes files a fresh checkout cannot reproduce.
func registerLoadFlags(fs *flag.FlagSet, dirUsage string) func() load.Config {
	dir := fs.String("dir", ".", dirUsage)
	scope := fs.String("scope", string(load.ScopeModule),
		"which modules are the main module, one of `module|workspace`: the one --dir is in, or every module go.work lists")
	build := registerBuildFlags(fs)
	return func() load.Config {
		return load.Config{Dir: *dir, Build: *build, Scope: load.ModuleScope(*scope), CLIVersion: version()}
	}
}

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
		"a comma-separated `tag,list` of additional build tags to consider satisfied during the load")
	fs.StringVar(&b.Mod, "mod", "",
		"module download `mode` to use: readonly, vendor or mod; defaults to the go command's own choice")
	fs.Var(pathFlag{&b.ModFile}, "modfile",
		"read an alternate go.mod `file` instead of the one in the module root directory")
	fs.Var(pathFlag{&b.Overlay}, "overlay",
		"read a JSON config `file` that provides an overlay for build operations")
	return &b
}

// pathFlag resolves a path flag against the caller's working directory at
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
	// Checked before the command is extracted, not after: the extraction
	// below deliberately leaves a leading flag with the default command, so
	// a `cmd == "-h"` test after it can never fire — which is exactly how
	// `servo -h` came to print `generate`'s four flags and exit 1, in the
	// one place a new user looks for the other twelve commands. --version
	// is the same story: left to generate's parser it printed generate's
	// usage twice and exited 1.
	//
	// A bare `servo` still generates. That is the documented contract, and
	// it is why only an explicit help or version request is intercepted.
	if len(args) > 0 && isHelpArg(args[0]) {
		topic := ""
		if len(args) > 1 && args[1] != "" && args[1][0] != '-' {
			topic = args[1]
		}
		return writeUsage(os.Stdout, topic)
	}
	if len(args) > 0 && isVersionArg(args[0]) {
		return printVersion(os.Stdout)
	}

	name := "generate"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		name = args[0]
		args = args[1:]
	}
	c := lookupCommand(name)
	if c == nil || c.setup == nil {
		return fmt.Errorf("servo: unknown command %q\n\n%s\nrun `servo help` for flags and usage", name, commandList())
	}
	return c.run(args)
}

// printVersion is `servo version` and `servo --version`: one line naming
// the servo version, the Go toolchain that built it, and its platform.
func printVersion(w io.Writer) error {
	_, err := fmt.Fprintf(w, "servo %s %s %s/%s\n", version(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return err
}
