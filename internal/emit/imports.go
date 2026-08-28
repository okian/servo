package emit

import (
	"fmt"
	"sort"
	"strings"
)

// ImportManager tracks every import path a generated file references and
// assigns each a call-site identifier, aliasing by path segment on
// collision — never a bare numeric suffix, since "config2" gives a reader
// no idea which config it is while "barconfig" does.
type ImportManager struct {
	byPath map[string]string // import path -> alias ("" = use the package's own name)
	byName map[string]string // identifier currently in use -> the import path that claimed it
}

func NewImportManager() *ImportManager {
	return &ImportManager{byPath: map[string]string{}, byName: map[string]string{}}
}

// Reserve claims an identifier that generated code hard-codes — a
// parameter name, a local, anything a user package must not be allowed to
// shadow. A package that wanted that identifier is aliased instead.
//
// It exists because emitted code references user types package-qualified
// (`chat.RoomKey`) inside functions whose own parameters and locals are
// fixed strings. A user package named `app` would otherwise take the
// identifier the scope constructor's `app *App` parameter needs, and the
// generated file would not compile — with `servo generate` reporting
// success, since it formats the output but does not type-check it.
func (m *ImportManager) Reserve(names ...string) {
	for _, name := range names {
		if _, taken := m.byName[name]; !taken {
			m.byName[name] = "\x00reserved"
		}
	}
}

// Add registers path (whose default package identifier is pkgName) and
// returns the identifier to use at call sites.
func (m *ImportManager) Add(path, pkgName string) string {
	if alias, ok := m.byPath[path]; ok {
		if alias == "" {
			return pkgName
		}
		return alias
	}
	if claimedBy, taken := m.byName[pkgName]; !taken || claimedBy == path {
		m.byPath[path] = ""
		m.byName[pkgName] = path
		return pkgName
	}
	alias := m.aliasFor(path)
	m.byPath[path] = alias
	m.byName[alias] = path
	return alias
}

// aliasFor extends path's identifier backward one segment at a time
// ("config" -> "barconfig" -> "foobarconfig" -> ...) until it no longer
// collides.
func (m *ImportManager) aliasFor(path string) string {
	segments := strings.Split(path, "/")
	for i := len(segments) - 2; i >= 0; i-- {
		candidate := sanitizeIdent(strings.Join(segments[i:], ""))
		if _, taken := m.byName[candidate]; !taken {
			return candidate
		}
	}
	return sanitizeIdent(strings.Join(segments, "_"))
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '.' || r == '-' {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "pkg"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	if shouldAvoidBare(out) {
		// A single-segment import path (or joined segments that happen to
		// spell one) can sanitize to a bare keyword, which is not a legal
		// import alias — or to a predeclared name ("new", "len", ...),
		// which is legal but shadows the builtin for every use site in
		// the file.
		out += "_"
	}
	return out
}

// RenderImports emits the import block, sorted by path so output is
// byte-stable regardless of registration order or map iteration.
func (m *ImportManager) RenderImports() string {
	type entry struct{ path, alias string }
	entries := make([]entry, 0, len(m.byPath))
	for path, alias := range m.byPath {
		entries = append(entries, entry{path, alias})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	var b strings.Builder
	b.WriteString("import (\n")
	for _, e := range entries {
		if e.alias == "" {
			fmt.Fprintf(&b, "\t%q\n", e.path)
		} else {
			fmt.Fprintf(&b, "\t%s %q\n", e.alias, e.path)
		}
	}
	b.WriteString(")\n")
	return b.String()
}
