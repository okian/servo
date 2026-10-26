package emit

import (
	"regexp"
	"strings"
	"testing"
)

// byteRuneSrc provides *Repo[[]byte] and *Index[rune] and consumes them as
// the identical *Repo[[]uint8] and *Index[int32], beside a value the caller
// supplies as []byte and a consumer takes as []uint8. Seed stands for the
// []byte a spec writes as servo.Value[[]byte]().
const byteRuneSrc = `
package app

type Repo[T any] struct{}

func NewRepo() *Repo[[]byte] { return &Repo[[]byte]{} }

type Index[K comparable] struct{}

func NewIndex() *Index[rune] { return &Index[rune]{} }

type Seed = []byte

type Server struct{}

func NewServer(r *Repo[[]uint8], ix *Index[int32], seed []uint8) *Server { return &Server{} }
`

// TestEmitByteAndUint8AreOneNode: byte and rune are aliases of uint8 and
// int32, so a constructor for *Repo[[]byte] satisfies a parameter written
// *Repo[[]uint8]; it used to fail with "no provider for *Repo[[]uint8]",
// and a servo.Value[[]byte] reached no []uint8 parameter either. The
// node's name, in the graph and in every report, spells uint8 and int32,
// as its key does. The code keeps what the declarations wrote, since it is
// rendered from their types, not from keys; either spelling compiles.
func TestEmitByteAndUint8AreOneNode(t *testing.T) {
	resolved, spec := buildFixture(t, fixtureSpec{roots: []string{"Server"}, values: []string{"Seed"}}, fixturePkg{"example.com/app", byteRuneSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		`{Type: "*example.com/app.Repo[[]uint8]", Level: 1,`,
		`{Type: "*example.com/app.Index[int32]", Level: 1,`,
		`{Type: "[]uint8", Level: 0,`,
		"server := NewServer(repoBytes, indexRune, v2)",
		"\tV []byte\n",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	// Matched loosely because gofmt aligns the struct's field types.
	for _, field := range []string{`repoBytes +\*Repo\[\[\]byte\]`, `indexRune +\*Index\[rune\]`, `v2 +\[\]byte`} {
		if !regexp.MustCompile(`(?m)^\t` + field + `$`).MatchString(gen) {
			t.Errorf("App has no field matching %s\n---\n%s", field, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/app", byteRuneSrc})
}
