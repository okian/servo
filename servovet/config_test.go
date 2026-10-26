package servovet

import (
	"strings"
	"testing"
)

func TestConfigDirectiveWellFormedIsQuiet(t *testing.T) {
	const src = `package fixture

//servo:config prefix=POSTGRES
type dbConfig struct {
	dsn string ` + "`config:\"dsn,required\"`" + `
}

var _ = dbConfig{}
`
	if got := runOn(t, src); len(got) != 0 {
		t.Fatalf("got %d diagnostics for a well-formed directive, want 0: %v", len(got), got)
	}
}

func TestConfigDirectiveTypoIsFlagged(t *testing.T) {
	// The defining hazard of a comment directive: misspelled, it is just a
	// comment — it compiles, generates, and silently loads nothing.
	const src = `package fixture

//servo:confg prefix=POSTGRES
type dbConfig struct {
	dsn string
}

var _ = dbConfig{}
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], `unknown //servo: directive "confg"`) || !strings.Contains(got[0], "route verbs") {
		t.Fatalf("got %v, want the unknown-directive diagnostic naming both families", got)
	}
}

func TestConfigDirectiveBadOptionsAreFlagged(t *testing.T) {
	const src = `package fixture

//servo:config prefix=postgres
type dbConfig struct {
	dsn string
}

var _ = dbConfig{}
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "must be UPPER_SNAKE") {
		t.Fatalf("got %v, want the prefix-case diagnostic", got)
	}
}

func TestConfigDirectiveOnNonStructIsFlagged(t *testing.T) {
	const src = `package fixture

//servo:config prefix=DB
type port int

var _ = port(0)
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "not a struct") {
		t.Fatalf("got %v, want the not-a-struct diagnostic", got)
	}
}

func TestConfigDirectiveMisplacedIsFlagged(t *testing.T) {
	// On a function (or any non-type declaration) the generator never
	// looks, so the author's config silently doesn't exist.
	const src = `package fixture

//servo:config prefix=DB
func loadConfig() {}
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "must be the doc comment of a type declaration") {
		t.Fatalf("got %v, want the misplaced-directive diagnostic", got)
	}
}

// The lines that fell between the generator's two scanners — each skipped
// what looked like the other's — now get the message `servo generate`
// prints for them, from the same pass.
func TestUnclaimedDirectivesAreFlagged(t *testing.T) {
	const src = `package fixture

//servo:config prefix=APP
var settings = 1

//servo:confg prefix=APP
var other = 2

//servo:get /floating
var floating = 3

//servo:
func Bare() {}

//servo:config prefix=GROUP
type (
	a struct{}
	b struct{}
)
`
	got := runOn(t, src)
	want := []string{
		"//servo:config must be the doc comment of a type declaration",
		`unknown //servo: directive "confg"`,
		"//servo:get must be the doc comment of a top-level function",
		"empty //servo: directive",
		"documents a group of type declarations",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d diagnostics, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("diagnostic %d = %q, want it to contain %q", i, got[i], w)
		}
	}
}

// One tokenizer means a tab, or a space after the colon, is read the way
// `servo generate` reads it: these are directives, not unrecognized names.
func TestDirectiveTokenizerMatchesTheGenerator(t *testing.T) {
	const src = "package fixture\n\n" +
		"import \"context\"\n\n" +
		"type Resp struct{}\n\n" +
		"//servo:get\t/tab\n" +
		"func Tab(ctx context.Context) (*Resp, error) { return nil, nil }\n\n" +
		"//servo: get /spaced\n" +
		"func Spaced(ctx context.Context) (*Resp, error) { return nil, nil }\n\n" +
		"//servo:config\tprefix=APP\n" +
		"type settings struct{ dsn string }\n\n" +
		"var _ = settings{}\n"
	if got := runOn(t, src); len(got) != 0 {
		t.Fatalf("got %d diagnostics for well-formed directives, want 0: %v", len(got), got)
	}
}

// A space after // turns a directive into a comment Go and servo both
// ignore; a comment that would be a well-formed directive without it is
// the near miss worth a squiggle. Prose that merely mentions servo: is not.
func TestNearMissDirectivesAreFlagged(t *testing.T) {
	const src = `package fixture

import "context"

type Resp struct{}

// servo:get /orders
func Orders(ctx context.Context) (*Resp, error) { return nil, nil }

//	servo:config prefix=APP
type settings struct{ dsn string }

// servo:get handlers return a response
func Prose() {}

// servo:config types are loaded by a generated function
type prose struct{}

var _, _ = settings{}, prose{}
`
	got := runOn(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (the two near misses, not the prose): %v", len(got), got)
	}
	for i, w := range []string{"write //servo:get /orders", "write //servo:config prefix=APP"} {
		if !strings.Contains(got[i], w) || !strings.Contains(got[i], "the space after //") {
			t.Errorf("diagnostic %d = %q, want it to say %q", i, got[i], w)
		}
	}
}

// Generated files quote directives in prose, so they are not searched for
// near misses at all.
func TestNearMissSkipsGeneratedFiles(t *testing.T) {
	const src = `// Code generated by servo. DO NOT EDIT.

package fixture

// servo:get /orders
func Orders() {}
`
	if got := runOn(t, src); len(got) != 0 {
		t.Fatalf("got %v in a generated file, want nothing", got)
	}
}

func TestOrdinaryCommentsAreNotDirectives(t *testing.T) {
	const src = `package fixture

// servo:config with a leading space is prose, matching go:generate's rule.
// And a mention of //servo:config mid-sentence is prose too.
type dbConfig struct {
	dsn string
}

var _ = dbConfig{}
`
	if got := runOn(t, src); len(got) != 0 {
		t.Fatalf("got %d diagnostics for prose comments, want 0: %v", len(got), got)
	}
}

func TestConfigFileMarkerRequiresBuildTag(t *testing.T) {
	const src = `package fixture

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.ConfigFile("config.yaml"),
	)
}
`
	got := runOn(t, src)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "servo.ConfigFile") {
		t.Fatalf("got %v, want servo.ConfigFile flagged in an untagged file", got)
	}
}
