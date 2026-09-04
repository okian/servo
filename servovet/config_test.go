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
	if len(got) != 1 || !strings.Contains(got[0], "unrecognized directive //servo:confg") {
		t.Fatalf("got %v, want the unrecognized-directive diagnostic", got)
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
	if len(got) != 1 || !strings.Contains(got[0], "not attached to a type declaration") {
		t.Fatalf("got %v, want the misplaced-directive diagnostic", got)
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
