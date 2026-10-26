package emit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryGoldenCarriesTheContractGuard: each emitter writes a file that
// names the runtime contract it was generated against, so a file newer
// than the servo go.mod pins fails on that line, beside a comment saying
// what to do. The goldens cover every emitter, the config loaders
// included, so a new one that forgets the guard fails here.
func TestEveryGoldenCarriesTheContractGuard(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("testdata", "golden", "*.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if len(goldens) == 0 {
		t.Fatal("no goldens found")
	}
	for _, path := range goldens {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"\t\"github.com/okian/servo/v3/servoimpl\"\n",
			"// This file needs github.com/okian/servo/v3 v3.5.0 or later.",
			"`go tool servo generate`",
			"\nconst _ = servoimpl.Contract1\n",
		} {
			if !strings.Contains(string(src), want) {
				t.Errorf("%s is missing %q", path, want)
			}
		}
	}
}

// TestContractGuardWinsItsIdentifier: servoimpl is registered before any
// user package, so a user package of that name is the one aliased, and a
// node named Servoimpl does not take the local that would hide the
// package from New's body.
func TestContractGuardWinsItsIdentifier(t *testing.T) {
	const implPkg = `
package servoimpl

type Clock struct{}

func NewClock() *Clock { return &Clock{} }
`
	const appSrc = `
package app

import "example.com/servoimpl"

type Servoimpl struct{}

func NewServoimpl(c *servoimpl.Clock) *Servoimpl { return &Servoimpl{} }
`
	resolved, spec := buildResolvedFromPkgs(t, []string{"Servoimpl"}, fixturePkg{"example.com/servoimpl", implPkg}, fixturePkg{"example.com/app", appSrc})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	gen := string(out)
	for _, want := range []string{
		"\t\"github.com/okian/servo/v3/servoimpl\"\n",
		`examplecomservoimpl "example.com/servoimpl"`,
		"const _ = servoimpl.Contract1",
		"servoimpl2 := NewServoimpl(clock)",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("generated source missing %q\n---\n%s", want, gen)
		}
	}
	typecheckGenerated(t, out, fixturePkg{"example.com/servoimpl", implPkg}, fixturePkg{"example.com/app", appSrc})
}
