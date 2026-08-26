package emit

import (
	"fmt"
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
	n := a.used[base]
	a.used[base] = n + 1
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s%d", base, n+1)
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
