package main

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
)

// commands is the list `servo help` prints, in the order the reference
// documents them: the two you run in CI first, then the four that answer a
// question about one graph, then the ones that write or diagnose.
//
// It is a list rather than the switch in run() so that an unknown command
// can print the alternatives. Being told `unknown command "geneate"` and
// nothing else is a dead end when the only place the names are written
// down is the website.
var commands = []struct{ Name, Usage, Summary string }{
	{"generate", "servo generate [--dir]", "resolve every injector found under --dir and write its generated file"},
	{"check", "servo check [--dir]", "verify every generated file matches a fresh generation; writes nothing"},
	{"graph", "servo graph [--dir] [--format=text|json|dot|mermaid]", "export one injector's resolved graph"},
	{"explain", "servo explain <type> [--dir] [--json]", "which provider was selected for a type, and why"},
	{"why", "servo why <type> [--dir] [--json]", "shortest path from a root to that node"},
	{"list", "servo list [--rejected] [--all] [--dir] [--json]", "the candidate index, or every excluded function and the rule that excluded it"},
	{"config", "servo config [--dir] [--json]", "every setting one injector's //servo:config types read: env name, file key, type, default"},
	{"init", "servo init [--dir] [--tags]", "scaffold a spec file with the correct build tag"},
	{"doctor", "servo doctor [--dir]", "diagnose setup problems before go generate ever runs"},
	{"migrate", "servo migrate [--dir]", "read v1 Register(X{}, N) calls and emit a v3 skeleton"},
	{"new", "servo new component|adapter|mock-adapter ...", "scaffold a component, a third-party wrapper, or a mock adapter"},
	{"version", "servo version", "the version of servo that built this binary"},
	{"help", "servo help [command]", "this list, or one command's usage"},
}

// buildFlagsHelp is appended to the general usage rather than repeated per
// command: the seven commands that load packages all take the same four,
// spelled exactly as `go build` spells them.
const buildFlagsHelp = `Build flags, accepted by every command that loads packages (generate, check,
graph, explain, why, list, config, doctor) with the same meaning as in go build:

    --tags, --mod, --modfile, --overlay

--tags resolves the graph under those tags and writes a correspondingly
constrained file, so one injector can hold a default and a variant side by
side.`

// isHelpArg reports whether arg asks for usage. All four spellings are
// accepted because all four are what people type, and the cost of guessing
// wrong is a new user concluding the tool has one command.
func isHelpArg(arg string) bool {
	switch arg {
	case "help", "-h", "--help", "-help":
		return true
	}
	return false
}

func writeUsage(w io.Writer, topic string) error {
	if topic != "" {
		for _, c := range commands {
			if c.Name == topic {
				_, err := fmt.Fprintf(w, "usage: %s\n\n%s\n", c.Usage, c.Summary)
				return err
			}
		}
		return fmt.Errorf("servo: unknown command %q\n\n%s", topic, commandList())
	}
	_, err := fmt.Fprintf(w, `servo %s — build-time dependency injection for Go.

usage: servo <command> [flags]

%s
generate is the default command, so a bare `+"`servo`"+` generates.

%s

Full reference: https://okian.github.io/servo/reference/
`, version(), commandList(), buildFlagsHelp)
	return err
}

func commandList() string {
	width := 0
	for _, c := range commands {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "    %-*s  %s\n", width, c.Name, c.Summary)
	}
	return b.String()
}

// version reports the version of servo this binary was built from.
//
// It matters more here than for most tools: servo writes files you commit
// and gates them with `servo check`, so two machines running different
// versions produce a diff that reads exactly like a forgotten regenerate.
// The stale message points at this command for that reason.
//
// A binary built straight from a working tree has no module version —
// debug.ReadBuildInfo reports "(devel)" — so the VCS stamp the go command
// embeds is used instead, which is the only thing that distinguishes two
// such builds from each other.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	v := info.Main.Version
	if v != "" && v != "(devel)" {
		return v
	}
	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return "(devel)"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if modified == "true" {
		return "(devel, " + revision + ", dirty)"
	}
	return "(devel, " + revision + ")"
}
