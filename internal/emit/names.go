package emit

import (
	"go/token"
	"go/types"
	"strconv"
	"strings"
	"unicode"
)

// NameAllocator derives short, readable Go identifiers from type names,
// deduping collisions with a readable numeric suffix ("pool", "pool2", ...)
// rather than an opaque counter.
type NameAllocator struct {
	// used is every name handed out or reserved — the names themselves,
	// not the bases they came from. Counting bases alone handed one name
	// out twice: pool, pool, pool2 gave "pool2" to the second pool and
	// then again to the base spelled pool2, and a traced method taking
	// (len int, len2 int) came out as (len2 int, len2 int).
	used map[string]bool
	// next is, per base, how many candidates have been tried for it, so a
	// base asked for again resumes where it stopped instead of walking
	// past every suffix it already handed out.
	next map[string]int
}

func NewNameAllocator() *NameAllocator {
	return &NameAllocator{used: make(map[string]bool), next: make(map[string]int)}
}

// Allocate returns a unique identifier for t, based on its own type name.
func (a *NameAllocator) Allocate(t types.Type) string {
	return a.AllocateName(baseName(t))
}

// AllocateName is Allocate for a name that was derived some other way — a
// scope's key type plus a suffix, say — with the identical deduping and
// keyword-avoidance rules.
//
// The candidates are base, base2, base3, …, and the first one nobody holds
// wins. A bare keyword ("range", "type", "select", ...) can never be
// emitted as an identifier, and a bare predeclared name ("new", "len",
// "error", ...) legally shadows the builtin for the rest of its scope,
// which is exactly the kind of thing worth never doing in generated code
// even though it would compile; either way that candidate is skipped, so
// the first allocation goes through the same numeric suffix a real
// collision would ("range" -> "range2", "new" -> "new2"). A suffixed
// candidate is held to the same rule, since "int3" plus a suffix is
// "int32".
func (a *NameAllocator) AllocateName(base string) string {
	for n := a.next[base]; ; n++ {
		candidate := base
		if n > 0 {
			candidate = base + strconv.Itoa(n+1)
		}
		if a.used[candidate] || shouldAvoidBare(candidate) {
			continue
		}
		a.next[base] = n + 1
		a.used[candidate] = true
		return candidate
	}
}

// Reserve claims names exactly as written, with no suffix and no keyword
// check: identifiers somebody else already declared, which an allocation
// has to step around rather than take. A later AllocateName of one of them
// gets the next free suffix.
func (a *NameAllocator) Reserve(names ...string) {
	for _, name := range names {
		a.used[name] = true
	}
}

// Free reports whether name is still unclaimed. It exists for callers that
// derive further identifiers from an allocated one — a field whose name
// also decides a method's — and so have to know a candidate is workable
// before they commit to it.
func (a *NameAllocator) Free(name string) bool {
	return !a.used[name]
}

// shouldAvoidBare reports whether name is a Go keyword or a predeclared
// identifier (a builtin type, function, or constant such as "int", "len",
// "new", "true", "nil") — legal to shadow as a local variable, but never
// worth doing where it can be trivially avoided.
func shouldAvoidBare(name string) bool {
	return token.IsKeyword(name) || types.Universe.Lookup(name) != nil
}

// qualifiedBaseName is the package name joined to the type name, for when
// the bare type name is ambiguous. It is the same judgement aliasFor makes
// about imports: nine packages each declaring a Config would otherwise
// take config, config2, ... config9, and a reader of the generated file
// could not tell which was which, where sessionConfig and apiConfig say so
// outright.
func qualifiedBaseName(t types.Type) string {
	named := unwrapToNamed(t)
	if named == nil || named.Obj().Pkg() == nil {
		return baseName(t)
	}
	return lowerFirst(named.Obj().Pkg().Name()) + capitalize(named.Obj().Name()) + typeArgsWord(named)
}

// packageNameOfType is the identifier a generated file refers to t's
// package by, or "" for a type with no package.
func packageNameOfType(t types.Type) string {
	named := unwrapToNamed(t)
	if named == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Name()
}

func baseName(t types.Type) string {
	named := unwrapToNamed(t)
	if named == nil {
		return "v"
	}
	return lowerFirst(named.Obj().Name()) + typeArgsWord(named)
}

// typeArgsWord spells an instantiated generic type's arguments into its
// name, so Repo[User] and Repo[Order] are repoUser and repoOrder rather
// than one base name the allocator has to number: repoRepo and repoRepo2
// said nothing about which was which, and which got the 2 depended on
// order. A non-generic type contributes nothing, so no other name changes.
func typeArgsWord(named *types.Named) string {
	args := named.TypeArgs()
	if args.Len() == 0 {
		return ""
	}
	var b strings.Builder
	for i := range args.Len() {
		b.WriteString(typeWord(args.At(i)))
	}
	return b.String()
}

// typeWord is one type argument as a capitalized identifier fragment.
func typeWord(t types.Type) string {
	switch u := types.Unalias(t).(type) {
	case *types.Named:
		return capitalize(u.Obj().Name()) + typeArgsWord(u)
	case *types.Pointer:
		return typeWord(u.Elem())
	case *types.Basic:
		return capitalize(u.Name())
	case *types.Slice:
		return typeWord(u.Elem()) + "s"
	case *types.Array:
		return typeWord(u.Elem()) + "s"
	case *types.Map:
		return typeWord(u.Key()) + typeWord(u.Elem()) + "Map"
	case *types.Interface:
		if u.Empty() {
			return "Any"
		}
		return "Iface"
	case *types.Signature:
		return "Func"
	case *types.Chan:
		return typeWord(u.Elem()) + "Chan"
	default:
		return "T"
	}
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
