package resolve

import (
	"fmt"
	"strings"
	"testing"
)

// nearMissAppSrc offers, for an interface and for a pointer, every shape
// of function the near-miss engine has to judge: refused functions that
// would have provided the type or its twin, refused ones that would not,
// and package-level variables that are not constructors of anything.
const nearMissAppSrc = `package app

type Store interface{ Get() string }

type Mem struct{}

func (*Mem) Get() string { return "" }

// NewMem would implement Store, but a variadic constructor is refused.
func NewMem(opts ...int) *Mem { return &Mem{} }

// Hosts and Index are refused for their result types, which implement
// nothing.
func Hosts() []string       { return nil }
func Index() map[string]int { return nil }

// Count is a variable of no function type and Hook a function returning
// nothing: neither reads like a constructor.
var Count int
var Hook = func() {}

type Config struct{}

type Option func(*Config)

// NewConfig is refused, and would only have provided the value twin of
// *Config anyway.
func NewConfig(opts ...Option) Config { return Config{} }
`

// nearMissDiskSrc implements app.Store structurally from outside the main
// module, where nothing is the reader's to change.
const nearMissDiskSrc = `package lib

type Disk struct{}

func (*Disk) Get() string { return "" }

func NewDisk() *Disk { return &Disk{} }
`

func nearMissSummary(got []NearMiss) []string {
	out := make([]string, len(got))
	for i, m := range got {
		out[i] = fmt.Sprintf("%s: %s — %s", m.Kind, m.Func, m.Reason)
	}
	return out
}

// TestNearMissesForAnInterfaceNameOnlyTheMainModulesImplementers: asked
// why nothing provides app.Store, the engine names the refused function
// whose result implements it, and nothing else — not a dependency's
// implementation, which is not the reader's to fix, nor the refused
// functions and variables that could never have provided a Store.
func TestNearMissesForAnInterfaceNameOnlyTheMainModulesImplementers(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", nearMissDiskSrc}, pkgSrc{"example.com/mod/app", nearMissAppSrc})
	in := m.input("example.com/mod/app")
	if len(in.Candidates) == 0 {
		t.Fatal("fixture has no accepted candidates; the engine's own result index would go untested")
	}

	got := nearMissSummary(NearMisses(in, m.typ("example.com/mod/app", "Store")))
	want := []string{"rejected: app.NewMem — variadic parameter — unsupported"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("NearMisses(app.Store) =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestNearMissesNameARefusedTwin: a refused function returning T is still
// the answer to "why is nothing providing *T" — the reader wrote it for
// that — and the reason says both what was refused and that the type is
// the other one of the pair.
func TestNearMissesNameARefusedTwin(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.org/lib", nearMissDiskSrc}, pkgSrc{"example.com/mod/app", nearMissAppSrc})

	got := nearMissSummary(NearMisses(m.input("example.com/mod/app"), m.ptr("example.com/mod/app", "Config")))
	want := []string{"rejected: app.NewConfig — variadic parameter — unsupported — and it returns app.Config, not *app.Config"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("NearMisses(*app.Config) =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
