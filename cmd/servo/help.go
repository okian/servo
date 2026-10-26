package main

import (
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"strings"
)

// buildFlagNames are the go build flags registerBuildFlags adds, in the
// order the reference lists them.
var buildFlagNames = []string{"tags", "mod", "modfile", "overlay"}

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

// isVersionArg reports whether arg is a request for the version in the
// spelling most CLIs accept. -v is deliberately not one of them: it is the
// conventional verbose switch, and a bare `servo -v` is a generate.
func isVersionArg(arg string) bool {
	return arg == "--version" || arg == "-version"
}

// writeUsage is `servo help`: the command list with the build flags, or,
// given a topic, that one command's help. An unknown topic is an error
// that still lists the real names — being told a name is wrong without
// being shown the right ones is a dead end when the only other place they
// are written down is the website.
func writeUsage(w io.Writer, topic string) error {
	if topic != "" {
		if c := lookupCommand(topic); c != nil {
			return c.writeHelp(w)
		}
		return fmt.Errorf("servo: unknown command %q\n\n%s", topic, commandList())
	}
	_, err := fmt.Fprintf(w, `servo %s — build-time dependency injection for Go.

usage: servo <command> [flags] [arguments]

%s
generate is the default command, so a bare `+"`servo`"+` generates. A command's
flags may come before or after its arguments; `+"`servo help <command>`"+` lists
them, as does `+"`servo <command> -h`"+`.

Build flags, the same as go build's, taken by every command that loads
packages (%s):

    --%s

--tags resolves the graph under those tags and writes a correspondingly
constrained file, so one injector can hold a default and a variant side by
side.

Full reference: https://okian.github.io/servo/reference/
`, version(), commandList(), strings.Join(loadingCommands(), ", "), strings.Join(buildFlagNames, ", --"))
	return err
}

// commandList is the name and summary of every command, aligned.
func commandList() string {
	width := 0
	for _, c := range commands {
		width = max(width, len(c.name))
	}
	var b strings.Builder
	b.WriteString("Commands:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "    %-*s  %s\n", width, c.name, c.summary)
	}
	return b.String()
}

// loadingCommands names the commands that take the build flags, read off
// their own flag sets so the sentence listing them cannot fall behind.
func loadingCommands() []string {
	var names []string
	for _, c := range commands {
		if c.flagSet().Lookup("mod") != nil {
			names = append(names, c.name)
		}
	}
	return names
}

// usage is c's usage line — one per form, for a command with several —
// naming every flag its parser accepts, in the order help lists them.
// Flags go first only by convention: the parser takes them anywhere.
func (c *command) usage() string {
	head := "servo " + c.name
	if !c.formsNameFlags {
		for _, f := range orderedFlags(c.flagSet()) {
			head += " " + flagSynopsis(f)
		}
	}
	if len(c.forms) == 0 {
		return "usage: " + head
	}
	lines := make([]string, len(c.forms))
	for i, form := range c.forms {
		lines[i] = head + " " + form
	}
	return "usage: " + strings.Join(lines, "\n       ")
}

// writeHelp is `servo help <command>` and `servo <command> -h`: the usage
// line, what the command is for, and every flag with its default.
func (c *command) writeHelp(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s\n", c.usage(), c.summary)
	if flags := orderedFlags(c.flagSet()); len(flags) > 0 {
		names := make([]string, len(flags))
		width := 0
		for i, f := range flags {
			placeholder, _ := flag.UnquoteUsage(f)
			names[i] = strings.TrimSpace(flagSpelling(f.Name) + " " + placeholder)
			width = max(width, len(names[i]))
		}
		b.WriteString("\nFlags:\n")
		for i, f := range flags {
			_, text := flag.UnquoteUsage(f)
			if f.DefValue != "" && f.DefValue != "false" {
				text += fmt.Sprintf(" (default %q)", f.DefValue)
			}
			fmt.Fprintf(&b, "  %-*s  %s\n", width, names[i], text)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// flagSynopsis renders one flag for a usage line: `[--json]` for a
// switch, `[--dir path]` for a flag taking a value, named by the
// back-quoted word in its usage string.
func flagSynopsis(f *flag.Flag) string {
	placeholder, _ := flag.UnquoteUsage(f)
	if placeholder == "" {
		return "[" + flagSpelling(f.Name) + "]"
	}
	return "[" + flagSpelling(f.Name) + " " + placeholder + "]"
}

// flagSpelling is how help writes a flag: `--dir` for a word, and `-v`
// for a single letter, the way go's own commands spell one. The parser
// takes either dash count for every flag; this is only how it is shown.
func flagSpelling(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// orderedFlags lists fs's flags the way help shows them: --dir first, as
// the one nearly every command shares; then the command's own, by name;
// then the build flags, in buildFlagNames' order.
func orderedFlags(fs *flag.FlagSet) []*flag.Flag {
	var out []*flag.Flag
	if f := fs.Lookup("dir"); f != nil {
		out = append(out, f)
	}
	fs.VisitAll(func(f *flag.Flag) {
		if f.Name != "dir" && !slices.Contains(buildFlagNames, f.Name) {
			out = append(out, f)
		}
	})
	for _, name := range buildFlagNames {
		if f := fs.Lookup(name); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// version reports the version of servo this binary was built from.
//
// It matters more here than for most tools: servo writes files you commit
// and gates them with `servo check`, so two machines running different
// versions produce a diff that reads exactly like a forgotten regenerate.
// The stale message points at this command for that reason.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(unknown)"
	}
	return versionOf(info)
}

// versionOf is version's decision, apart from reading the build info, so
// each kind of build can be described without building a binary for it.
//
// A binary built straight from a working tree has no module version —
// the build info reports "(devel)" — so the VCS stamp the go command
// embeds is used instead, which is the only thing that distinguishes two
// such builds from each other.
//
// A replaced module is the other case the version alone gets wrong. `go
// tool servo` in a module that replaces servo — a fork, a local checkout,
// this repository's own examples — records the *requirement* as the main
// module's version and what was actually built under Replace, so
// examples/variants, which requires v3.0.0 and builds from ../.., printed
// v3.0.0: a release whose generator never ran there.
func versionOf(info *debug.BuildInfo) string {
	if r := info.Main.Replace; r != nil {
		if r.Version != "" && r.Version != "(devel)" {
			return r.Version + " (" + r.Path + ")"
		}
		return "(devel, replaced by " + r.Path + ")"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
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

// releaseVersion is the module version this binary was built at — what
// `go install …@v3.4.1` and `go tool servo` stamp — or "" for a build that
// carries none: a `go run` or a working-tree build ("(devel)"), or a
// replaced module, whose stamp names a requirement rather than what was
// built. It is the half of version() that can be compared with a go.mod
// requirement; the VCS stamp and a replacement's directory cannot.
func releaseVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return releaseVersionOf(info)
}

func releaseVersionOf(info *debug.BuildInfo) string {
	if info.Main.Replace != nil {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return ""
}
