package graph

import (
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestParseDirective(t *testing.T) {
	cases := []struct {
		comment string
		name    string
		args    []string
		ok      bool
	}{
		{"//servo:config prefix=DB", "config", []string{"prefix=DB"}, true},
		{"//servo:config", "config", nil, true},
		{"//servo:confg x", "confg", []string{"x"}, true},
		// A tab is whitespace like any other: generate used to serve
		// //servo:get<TAB>/orders while servovet called it unrecognized.
		{"//servo:get\t/orders", "get", []string{"/orders"}, true},
		{"//servo:get /orders  telemetry ", "get", []string{"/orders", "telemetry"}, true},
		// And so is a space after the colon: generate served this route
		// while servovet skipped the line as no directive at all.
		{"//servo: get /x", "get", []string{"/x"}, true},
		// A bare prefix is a directive with no name, which every reader
		// refuses — not a comment to skip.
		{"//servo:", "", nil, true},
		{"//servo:   ", "", nil, true},
		{"// servo:config prefix=DB", "", nil, false}, // a space after // makes it prose, as for go:generate
		{"// an ordinary comment", "", nil, false},
		{"/*servo:get /x*/", "", nil, false},
	}
	for _, tc := range cases {
		name, args, ok := ParseDirective(tc.comment)
		if name != tc.name || !slices.Equal(args, tc.args) || ok != tc.ok {
			t.Errorf("ParseDirective(%q) = %q, %q, %v; want %q, %q, %v", tc.comment, name, args, ok, tc.name, tc.args, tc.ok)
		}
	}
}

const directivesSrc = `// Package app carries a //servo: line in every place one can sit.
//servo:get /on-the-package
package app

//servo:get /orders
func Orders() {}

//servo:config prefix=APP
type settings struct{}

type (
	//servo:config prefix=INNER
	inner struct{}

	other struct{}
)

//servo:config prefix=GROUP
type (
	a struct{}
	b struct{}
)

//servo:config prefix=VAR
var v = 1

type T struct {
	//servo:config prefix=FIELD
	f int
}

//servo:get /method
func (T) M() {}

//servo:confg prefix=TYPO
type typo struct{}

//servo:
func Bare() {}

//servo:get /floating

func Body() {
	//servo:post /inside
}

//servo:config prefix=FUNC
func Load() {}
`

// TestFileDirectivesClaimsEveryLine walks the one pass over a file with a
// //servo: line in every position: each is either claimed by the scanner
// that reads it or reported, and none is skipped.
func TestFileDirectivesClaimsEveryLine(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", directivesSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	type outcome struct {
		route, config bool
		problem       string
	}
	want := map[int]outcome{
		2:  {problem: "//servo:get must be the doc comment of a top-level function"},
		5:  {route: true},
		8:  {config: true},
		12: {config: true},
		18: {problem: "documents a group of type declarations"},
		24: {problem: "//servo:config must be the doc comment of a type declaration"},
		28: {problem: "//servo:config must be the doc comment of a type declaration"},
		32: {route: true}, // a method: route refuses it with the better message
		35: {problem: `unknown //servo: directive "confg"`},
		38: {problem: "empty //servo: directive"},
		41: {problem: "//servo:get must be the doc comment of a top-level function"},
		44: {problem: "//servo:post must be the doc comment of a top-level function"},
		47: {problem: "//servo:config must be the doc comment of a type declaration"},
	}

	got := map[int]bool{}
	for _, d := range FileDirectives(f) {
		line := fset.Position(d.Comment.Pos()).Line
		got[line] = true
		w, ok := want[line]
		if !ok {
			t.Errorf("line %d: unexpected directive %q", line, d.Comment.Text)
			continue
		}
		if d.IsRoute() != w.route || d.IsConfig() != w.config {
			t.Errorf("line %d (%q): IsRoute=%v IsConfig=%v, want %v/%v", line, d.Comment.Text, d.IsRoute(), d.IsConfig(), w.route, w.config)
		}
		problem := d.Unclaimed()
		switch {
		case w.problem == "" && problem != "":
			t.Errorf("line %d: claimed, but Unclaimed() = %q", line, problem)
		case w.problem != "" && !strings.Contains(problem, w.problem):
			t.Errorf("line %d: Unclaimed() = %q, want it to contain %q", line, problem, w.problem)
		}
	}
	for line := range want {
		if !got[line] {
			t.Errorf("line %d: no directive found", line)
		}
	}
}

// The prose rule holds in the pass too: a mention mid-line, or a space
// after //, is never a directive.
func TestFileDirectivesIgnoresProse(t *testing.T) {
	const src = `package app

// servo:config prefix=APP is prose, matching go:generate's rule.
// A mention of //servo:config mid-sentence is prose too.
type settings struct{}
`
	f, err := parser.ParseFile(token.NewFileSet(), "app.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := FileDirectives(f); len(got) != 0 {
		t.Fatalf("got %d directives in prose, want 0", len(got))
	}
}

// The group-name grammar is written once, here, for both of its readers:
// internal/load's servo.Group and internal/route's directive token.
func TestGroupNamePattern(t *testing.T) {
	re := regexp.MustCompile(GroupNamePattern)
	for _, ok := range []string{"telemetry", "internal-2", "a_b", "X"} {
		if !re.MatchString(ok) {
			t.Errorf("%q rejected, want accepted", ok)
		}
	}
	for _, bad := range []string{"", "x y", "a.b", "über", "a/b"} {
		if re.MatchString(bad) {
			t.Errorf("%q accepted, want rejected", bad)
		}
	}
}
