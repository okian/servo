// Package legacy stands in for a pre-servo codebase: components register
// themselves with a global sequencer and a hand-maintained order, the
// pattern servo migrate reads to produce a starting spec file. Cache and
// DB deliberately share order 2 — a duplicate servo migrate's report is
// meant to catch, not a mistake in this fixture.
package legacy

type Logger struct{}
type DB struct{}
type Cache struct{}
type Server struct{}

func setup() {
	Register(&Logger{}, 1)
	Register(&DB{}, 2)
	Register(&Cache{}, 2)
	Register(&Server{}, 3)
}

// Register stands in for v1's real global-registry function. servo migrate
// only parses the syntax of calls shaped like this one — it never resolves
// what Register actually did — so this body is never called by anything
// and exists only to make the package build.
func Register(component any, order int) {}
