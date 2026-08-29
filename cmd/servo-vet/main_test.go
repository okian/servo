package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInheritedTagsRefusal pins both halves of the -tags contract: the
// flag is refused wherever and however it is spelled, and a bare -tags
// with nothing after it is not (there is no configuration to mistake it
// for, and go/analysis's own parser will report it).
func TestInheritedTagsRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		reject bool
	}{
		{"equals form", []string{"-tags=prod", "./..."}, true},
		{"double dash", []string{"--tags=prod", "./..."}, true},
		{"separate value", []string{"-tags", "prod", "./..."}, true},
		{"after the package list", []string{"./...", "-tags=prod"}, true},
		{"empty value", []string{"-tags=", "./..."}, false},
		{"trailing with no value", []string{"./...", "-tags"}, false},
		{"absent", []string{"./..."}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, reject := inheritedTagsRefusal(tc.args)
			if reject != tc.reject {
				t.Fatalf("inheritedTagsRefusal(%q) = %v, want %v", tc.args, reject, tc.reject)
			}
			if !reject {
				return
			}
			// The message has to name the invocation that works, or the
			// refusal is just an obstacle.
			for _, want := range []string{"go vet", "-vettool", "-tags=prod"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal does not mention %q:\n%s", want, msg)
				}
			}
		})
	}
}

// TestTagsRefusalStopsTheRealBinary checks the half of the contract that
// only exists once inheritedTagsRefusal's answer has been turned into an
// exit status, which is main()'s entire job. A refusal that printed the
// right paragraph and still exited 0 would be worse than no check at all:
// `go vet -vettool=servo-vet` and every CI step behind it read the exit
// code, not the prose, so the run would go green having analysed nothing
// the caller asked for. It also has to be a refusal rather than an
// analysis — reaching singlechecker.Main at all is the failure this
// guards against — and the message belongs on stderr, where a wrapper
// capturing vet's stdout will not swallow it.
//
// This has to be a subprocess: main() calls os.Exit, so anything in-process
// would take the test binary down with it. Building and running the real
// thing is the pattern the rest of this repo already uses wherever the unit
// under test is a whole program (see cmd/servo's fixture builds). Being a
// subprocess, it moves no coverage number — main() stays uncovered by
// construction, exactly as codecov.yml describes.
func TestTagsRefusalStopsTheRealBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "servo-vet")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-tags=prod", "./...")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("servo-vet -tags=prod ./... = %v, want a non-zero exit", err)
	}
	if got := exit.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2\nstderr:\n%s", got, stderr.String())
	}
	for _, want := range []string{"-tags does not work here", "go vet", "-vettool", "-tags=prod"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q:\n%s", want, stderr.String())
		}
	}
	if stdout.Len() != 0 {
		t.Errorf("the refusal was written to stdout, where a wrapper capturing vet's output would lose it: %q", stdout.String())
	}
}
