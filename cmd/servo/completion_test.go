package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func completionScript(t *testing.T, shell string) string {
	t.Helper()
	var b bytes.Buffer
	if err := runCompletion(&b, shell); err != nil {
		t.Fatalf("servo completion %s: %v", shell, err)
	}
	return b.String()
}

// TestCompletionScriptsAreReadOffTheTable: every command and every flag
// it takes is in every script, because each script is generated from the
// table the parser uses — the property that keeps a flag added later
// from going uncompleted.
func TestCompletionScriptsAreReadOffTheTable(t *testing.T) {
	for _, shell := range completionShells {
		script := completionScript(t, shell)
		for _, c := range commands {
			if !strings.Contains(script, c.name) {
				t.Errorf("%s script does not mention command %s", shell, c.name)
			}
			c.flagSet().VisitAll(func(f *flag.Flag) {
				spelled := flagSpelling(f.Name)
				if shell == "fish" {
					spelled = "-l " + f.Name
					if len(f.Name) == 1 {
						spelled = "-s " + f.Name
					}
				}
				if !strings.Contains(script, spelled) {
					t.Errorf("%s script does not complete %s's %s", shell, c.name, spelled)
				}
			})
		}
	}
	if err := runCompletion(&bytes.Buffer{}, "powershell"); err == nil || !strings.Contains(err.Error(), "bash, zsh, fish") {
		t.Errorf("an unknown shell = %v, want an error naming the three", err)
	}
}

// TestBashCompletionCompletes drives the bash script the way bash does —
// COMP_WORDS and COMP_CWORD set, the function called — and checks what it
// offers.
func TestBashCompletionCompletes(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	script := filepath.Join(t.TempDir(), "servo.bash")
	if err := os.WriteFile(script, []byte(completionScript(t, "bash")), 0o644); err != nil {
		t.Fatal(err)
	}
	complete := func(words ...string) []string {
		t.Helper()
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
		}
		prog := "source " + script + "\nCOMP_WORDS=(" + strings.Join(quoted, " ") + ")\nCOMP_CWORD=" +
			strconv.Itoa(len(words)-1) + "\n_servo\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
		out, err := exec.Command(bash, "-c", prog).CombinedOutput()
		if err != nil {
			t.Fatalf("bash completion of %q: %v\n%s", words, err, out)
		}
		return strings.Fields(string(out))
	}

	cases := []struct {
		words []string
		want  []string
	}{
		{[]string{"servo", "ex"}, []string{"explain"}},
		{[]string{"servo", "explain", "--j"}, []string{"--json"}},
		{[]string{"servo", "why", "--a"}, []string{"--all"}},
		{[]string{"servo", "graph", "--format", ""}, []string{"text", "json", "dot", "mermaid"}},
		{[]string{"servo", "graph", "--format", "=", "m"}, []string{"mermaid"}},
		{[]string{"servo", "new", ""}, []string{"component", "adapter", "mock-adapter"}},
		{[]string{"servo", "new", "mock-adapter", "mo"}, []string{"moq", "mockery", "mockgen"}},
		{[]string{"servo", "help", "ch"}, []string{"check"}},
		{[]string{"servo", "completion", ""}, []string{"bash", "zsh", "fish"}},
		// The value after --dir, split from it at the = as bash splits it,
		// is not taken for the command name.
		{[]string{"servo", "--dir", "=", "x", "ch"}, []string{"check"}},
		{[]string{"servo", "--dir", "x", "doc"}, []string{"doctor"}},
		// A leading flag means the default command, generate.
		{[]string{"servo", "--o"}, []string{"--overlay"}},
	}
	for _, c := range cases {
		got := complete(c.words...)
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("completing %q = %q, want %q", c.words, got, want)
		}
	}
}

// TestZshAndFishCompletionParse: their completion machinery cannot be
// driven outside an interactive shell, so the scripts are held to the
// shells' own syntax checks where those shells are installed.
func TestZshAndFishCompletionParse(t *testing.T) {
	for shell, check := range map[string][]string{"zsh": {"-n"}, "fish": {"--no-execute"}} {
		bin, err := exec.LookPath(shell)
		if err != nil {
			t.Logf("%s is not installed; skipping its syntax check", shell)
			continue
		}
		script := filepath.Join(t.TempDir(), "servo."+shell)
		if err := os.WriteFile(script, []byte(completionScript(t, shell)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(bin, append(check, script)...).CombinedOutput(); err != nil {
			t.Errorf("%s rejects its completion script: %v\n%s", shell, err, out)
		}
	}
}

// TestRunCompletionDispatch: the command reaches the script through the
// table like any other, and refuses a missing or unknown shell.
func TestRunCompletionDispatch(t *testing.T) {
	var err error
	out := captureStdout(t, func() { err = run([]string{"completion", "fish"}) })
	if err != nil || !strings.HasPrefix(out, "# fish completion for servo") {
		t.Fatalf("servo completion fish = %v:\n%s", err, out)
	}
	if err := run([]string{"completion"}); err == nil || !strings.Contains(err.Error(), "usage: servo completion <bash|zsh|fish>") {
		t.Errorf("servo completion with no shell = %v, want its usage", err)
	}
}
