package emit

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"
	"unicode"
)

// NameAllocator derives short, readable Go identifiers from type names,
// deduping collisions with a readable numeric suffix ("pool", "pool2", ...)
// rather than an opaque counter.
type NameAllocator struct {
	used map[string]int
}

func NewNameAllocator() *NameAllocator {
	return &NameAllocator{used: make(map[string]int)}
}

// Allocate returns a unique identifier for t, based on its own type name.
func (a *NameAllocator) Allocate(t types.Type) string {
	base := baseName(t)
	n, seen := a.used[base]
	if !seen && shouldAvoidBare(base) {
		// A bare keyword ("range", "type", "select", ...) can never be
		// emitted as an identifier, and a bare predeclared name ("new",
		// "len", "error", ...) legally shadows the builtin for the rest of
		// its scope, which is exactly the kind of thing worth never doing
		// in generated code even though it would compile. Either way,
		// seed the count as if one instance already existed, so the first
		// allocation goes through the same numeric-suffix path a real
		// collision would ("range" -> "range2", "new" -> "new2").
		n = 1
	}
	a.used[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s%d", base, n+1)
}

// shouldAvoidBare reports whether name is a Go keyword or a predeclared
// identifier (a builtin type, function, or constant such as "int", "len",
// "new", "true", "nil") — legal to shadow as a local variable, but never
// worth doing where it can be trivially avoided.
func shouldAvoidBare(name string) bool {
	return token.IsKeyword(name) || types.Universe.Lookup(name) != nil
}

func baseName(t types.Type) string {
	named := unwrapToNamed(t)
	if named == nil {
		return "v"
	}
	return lowerFirst(named.Obj().Name())
}

func unwrapToNamed(t types.Type) *types.Named {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return u
	case *types.Pointer:
		return unwrapToNamed(u.Elem())
	default:
		return nil
	}
}

// lowerFirst lowercases s for use as an identifier. An all-uppercase name
// (an acronym like "DB" or "URL") lowercases entirely, to "db"/"url"
// instead of the stranger "dB"/"uRL"; anything else just gets its first
// rune lowered.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	if isAllUpper(s) {
		return strings.ToLower(s)
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func isAllUpper(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
