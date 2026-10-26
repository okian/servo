package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// completionShells are the shells `servo completion` writes a script for.
var completionShells = []string{"bash", "zsh", "fish"}

func init() {
	lookupCommand("completion").setup = func(*flag.FlagSet) func([]string) error {
		return func(args []string) error { return runCompletion(os.Stdout, args[0]) }
	}
}

// flagValues are the closed sets some flags take, offered when completing
// their value; the others complete a directory, a file, or nothing.
var flagValues = map[string][]string{
	"format": {"text", "json", "dot", "mermaid"},
	"mod":    {"readonly", "vendor", "mod"},
}

// newKinds and mockTools are new's positional vocabularies.
var (
	newKinds  = []string{"component", "adapter", "mock-adapter"}
	mockTools = []string{"moq", "mockery", "gomock", "mockgen"}
)

// runCompletion writes the completion script for shell. Every name in it
// — commands, their flags, which flags take a value — is read off the
// command table and the flag sets the parser uses, so a flag added to a
// command completes without anyone editing a script.
func runCompletion(w io.Writer, shell string) error {
	switch shell {
	case "bash":
		return writeBashCompletion(w)
	case "zsh":
		return writeZshCompletion(w)
	case "fish":
		return writeFishCompletion(w)
	}
	return fmt.Errorf("servo completion: unknown shell %q (want %s)", shell, strings.Join(completionShells, ", "))
}

// completionCommand is one command as the scripts need it.
type completionCommand struct {
	name, summary string
	flags         []*flag.Flag
}

func completionCommands() []completionCommand {
	var out []completionCommand
	for _, c := range commands {
		out = append(out, completionCommand{c.name, c.summary, orderedFlags(c.flagSet())})
	}
	return out
}

// takesValue reports whether f is a flag with an argument rather than a
// switch.
func takesValue(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !b.IsBoolFlag()
}

// valueFlagNames is every flag, across all commands, that takes a value:
// the words a script must skip when looking for the command name.
func valueFlagNames() []string {
	var names []string
	for _, c := range completionCommands() {
		for _, f := range c.flags {
			if takesValue(f) && !slices.Contains(names, f.Name) {
				names = append(names, f.Name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// pathFlags are the value flags that name a directory or a file.
var (
	dirFlags  = []string{"dir"}
	fileFlags = []string{"modfile", "overlay"}
)

func commandNames() []string {
	var names []string
	for _, c := range commands {
		names = append(names, c.name)
	}
	return names
}

func writeBashCompletion(w io.Writer) error {
	var b strings.Builder
	dashed := func(names []string) string {
		var alts []string
		for _, n := range names {
			alts = append(alts, "--"+n, "-"+n)
		}
		return strings.Join(alts, "|")
	}
	b.WriteString(`# bash completion for servo, written by ` + "`servo completion bash`" + `.
# Load it in this shell with:  source <(servo completion bash)
_servo() {
	local cur=${COMP_WORDS[COMP_CWORD]} prev=${COMP_WORDS[COMP_CWORD-1]}
	local cmd= w skip= i
	local -a pos=()
	for ((i = 1; i < COMP_CWORD; i++)); do
		w=${COMP_WORDS[i]}
		# COMP_WORDBREAKS splits --dir=x into --dir, =, x: the = keeps the
		# value after it skipped.
		if [[ $w == = ]]; then
			skip=1
			continue
		fi
		if [[ -n $skip ]]; then
			skip=
			continue
		fi
		case $w in
		-*=*) ;;
`)
	fmt.Fprintf(&b, "\t\t%s) skip=1 ;;\n", dashed(valueFlagNames()))
	b.WriteString(`		-*) ;;
		*) if [[ -z $cmd ]]; then cmd=$w; else pos+=("$w"); fi ;;
		esac
	done
	if [[ $prev == = ]]; then
		prev=${COMP_WORDS[COMP_CWORD-2]}
	fi
	case $prev in
`)
	fmt.Fprintf(&b, "\t%s) COMPREPLY=($(compgen -d -- \"$cur\")); return ;;\n", dashed(dirFlags))
	fmt.Fprintf(&b, "\t%s) COMPREPLY=($(compgen -f -- \"$cur\")); return ;;\n", dashed(fileFlags))
	for _, name := range sortedKeys(flagValues) {
		fmt.Fprintf(&b, "\t%s) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n", dashed([]string{name}), strings.Join(flagValues[name], " "))
	}
	var others []string
	for _, n := range valueFlagNames() {
		if !slices.Contains(dirFlags, n) && !slices.Contains(fileFlags, n) && flagValues[n] == nil {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(&b, "\t%s) return ;;\n", dashed(others))
	}
	fmt.Fprintf(&b, `	esac
	if [[ -z $cmd && $cur != -* ]]; then
		COMPREPLY=($(compgen -W %q -- "$cur"))
		return
	fi
	local flags=
	case ${cmd:-generate} in
`, strings.Join(commandNames(), " "))
	for _, c := range completionCommands() {
		var names []string
		for _, f := range c.flags {
			names = append(names, flagSpelling(f.Name))
		}
		names = append(names, "--help")
		fmt.Fprintf(&b, "\t%s) flags=%q ;;\n", c.name, strings.Join(names, " "))
	}
	fmt.Fprintf(&b, `	esac
	if [[ $cur == -* ]]; then
		COMPREPLY=($(compgen -W "$flags" -- "$cur"))
		return
	fi
	case $cmd in
	help) COMPREPLY=($(compgen -W %q -- "$cur")) ;;
	completion) COMPREPLY=($(compgen -W %q -- "$cur")) ;;
	new)
		if ((${#pos[@]} == 0)); then
			COMPREPLY=($(compgen -W %q -- "$cur"))
		elif ((${#pos[@]} == 1)) && [[ ${pos[0]} == mock-adapter ]]; then
			COMPREPLY=($(compgen -W %q -- "$cur"))
		fi
		;;
	esac
}
complete -F _servo servo
`, strings.Join(commandNames(), " "), strings.Join(completionShells, " "), strings.Join(newKinds, " "), strings.Join(mockTools, " "))
	_, err := io.WriteString(w, b.String())
	return err
}

func writeZshCompletion(w io.Writer) error {
	var b strings.Builder
	b.WriteString(`#compdef servo
# zsh completion for servo, written by ` + "`servo completion zsh`" + `.
# Load it in this shell with:  source <(servo completion zsh)
# or save it as _servo in a directory on $fpath.
_servo() {
	local curcontext=$curcontext state line
	local -a commands
	commands=(
`)
	for _, c := range completionCommands() {
		fmt.Fprintf(&b, "\t\t%s\n", zshQuote(c.name+":"+c.summary))
	}
	b.WriteString(`	)
	_arguments -C '1: :->command' '*:: :->args' && return
	case $state in
	command)
		_describe -t commands 'servo command' commands
		;;
	args)
		case $words[1] in
`)
	for _, c := range completionCommands() {
		specs := []string{"'(- *)'{-h,--help}'[show this help]'"}
		for _, f := range c.flags {
			_, usage := flag.UnquoteUsage(f)
			desc := zshEscapeBrackets(usage)
			name := flagSpelling(f.Name)
			switch {
			case !takesValue(f):
				specs = append(specs, zshQuote(name+"["+desc+"]"))
			case slices.Contains(dirFlags, f.Name):
				specs = append(specs, zshQuote(name+"=["+desc+"]:directory:_files -/"))
			case slices.Contains(fileFlags, f.Name):
				specs = append(specs, zshQuote(name+"=["+desc+"]:file:_files"))
			case flagValues[f.Name] != nil:
				specs = append(specs, zshQuote(name+"=["+desc+"]:value:("+strings.Join(flagValues[f.Name], " ")+")"))
			default:
				specs = append(specs, zshQuote(name+"=["+desc+"]:value: "))
			}
		}
		switch c.name {
		case "explain", "why":
			specs = append(specs, "':type: '")
		case "help":
			specs = append(specs, zshQuote("1:command:("+strings.Join(commandNames(), " ")+")"))
		case "completion":
			specs = append(specs, zshQuote("1:shell:("+strings.Join(completionShells, " ")+")"))
		case "new":
			specs = append(specs, zshQuote("1:kind:("+strings.Join(newKinds, " ")+")"), zshQuote("2:tool or type name:->newarg"), "'3:type name: '")
		}
		fmt.Fprintf(&b, "\t\t%s)\n\t\t\t_arguments %s\n", c.name, strings.Join(specs, " \\\n\t\t\t\t"))
		if c.name == "new" {
			fmt.Fprintf(&b, "\t\t\t[[ $state == newarg && $words[2] == mock-adapter ]] && _values tool %s\n", strings.Join(mockTools, " "))
		}
		b.WriteString("\t\t\t;;\n")
	}
	b.WriteString(`		esac
		;;
	esac
}
if [[ $zsh_eval_context[-1] == loadautofunc ]]; then
	_servo "$@"
else
	compdef _servo servo
fi
`)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeFishCompletion(w io.Writer) error {
	var b strings.Builder
	b.WriteString("# fish completion for servo, written by `servo completion fish`.\n")
	b.WriteString("# Load it in this shell with:  servo completion fish | source\n")
	b.WriteString("complete -c servo -f\n")
	all := strings.Join(commandNames(), " ")
	for _, c := range completionCommands() {
		fmt.Fprintf(&b, "complete -c servo -n %s -a %s -d %s\n", fishQuote("not __fish_seen_subcommand_from "+all), c.name, fishQuote(c.summary))
	}
	for _, c := range completionCommands() {
		cond := fishQuote("__fish_seen_subcommand_from " + c.name)
		fmt.Fprintf(&b, "complete -c servo -n %s -s h -l help -d %s\n", cond, fishQuote("show this command's help"))
		for _, f := range c.flags {
			_, usage := flag.UnquoteUsage(f)
			option := "-l " + f.Name
			if len(f.Name) == 1 {
				option = "-s " + f.Name
			}
			line := fmt.Sprintf("complete -c servo -n %s %s -d %s", cond, option, fishQuote(usage))
			switch {
			case !takesValue(f):
			case slices.Contains(dirFlags, f.Name):
				line += " -r -a '(__fish_complete_directories)'"
			case slices.Contains(fileFlags, f.Name):
				line += " -r -F"
			case flagValues[f.Name] != nil:
				line += " -x -a " + fishQuote(strings.Join(flagValues[f.Name], " "))
			default:
				line += " -x"
			}
			b.WriteString(line + "\n")
		}
	}
	fmt.Fprintf(&b, "complete -c servo -n %s -a %s\n", fishQuote("__fish_seen_subcommand_from help"), fishQuote(all))
	fmt.Fprintf(&b, "complete -c servo -n %s -a %s\n", fishQuote("__fish_seen_subcommand_from completion"), fishQuote(strings.Join(completionShells, " ")))
	fmt.Fprintf(&b, "complete -c servo -n %s -a %s\n", fishQuote("__fish_seen_subcommand_from new; and not __fish_seen_subcommand_from "+strings.Join(newKinds, " ")), fishQuote(strings.Join(newKinds, " ")))
	fmt.Fprintf(&b, "complete -c servo -n %s -a %s\n", fishQuote("__fish_seen_subcommand_from mock-adapter; and not __fish_seen_subcommand_from "+strings.Join(mockTools, " ")), fishQuote(strings.Join(mockTools, " ")))
	_, err := io.WriteString(w, b.String())
	return err
}

// zshQuote single-quotes s for a zsh script.
func zshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// zshEscapeBrackets escapes the characters _arguments gives meaning to
// inside an option's [description].
func zshEscapeBrackets(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`, ":", `\:`).Replace(s)
}

// fishQuote single-quotes s for a fish script.
func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
