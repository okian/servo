// Command servo is the build-time dependency and lifecycle generator's
// CLI: resolve, diagnose, emit, and inspect.
package main

import (
	"flag"
	"fmt"
	"os"
)

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
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runGenerate(*dir)

	case "check":
		fs := flag.NewFlagSet("check", flag.ContinueOnError)
		dir := fs.String("dir", ".", "directory to scan from; every injector found within it is checked")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runCheck(*dir)

	case "graph":
		fs := flag.NewFlagSet("graph", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		format := fs.String("format", "text", "text|json|dot|mermaid")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runGraph(*dir, *format)

	case "explain":
		fs := flag.NewFlagSet("explain", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: servo explain [--json] <type>")
		}
		return runExplain(*dir, fs.Arg(0), *jsonOut)

	case "why":
		fs := flag.NewFlagSet("why", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: servo why [--json] <type>")
		}
		return runWhy(*dir, fs.Arg(0), *jsonOut)

	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		rejected := fs.Bool("rejected", false, "list excluded functions and why")
		all := fs.Bool("all", false, "include stdlib/third-party, not just the main module")
		jsonOut := fs.Bool("json", false, "machine-readable output")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runList(*dir, *rejected, *all, *jsonOut)

	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		dir := fs.String("dir", ".", "directory to scaffold the spec file in")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runInit(*dir)

	case "doctor":
		fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
		dir := fs.String("dir", ".", "module directory")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return runDoctor(*dir)

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
