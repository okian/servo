package main

import (
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
