package graph

import (
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// SourceOrder orders positions the same way on every machine: by the
// module a file belongs to, then the file's slash-separated path within
// that module, then line and column.
//
// ComparePos compares raw filenames, and a raw filename carries the
// directory the module happened to be read from — a checkout, a replace
// directory, the module cache — plus the platform's separator. Two
// machines then disagree about the order of two declarations in different
// modules: moving a module that a servo.Include names from ../a-shared to
// ../z-shared swapped two trace wrappers in the committed file, and `\`
// sorts after letters where `/` sorts before them. Every order that reaches
// generated output goes through this instead.
//
// A position outside every known module falls back to its slash-separated
// filename, after all module positions: that is the standard library and
// anything else read from export data, which never decides the order of
// anything written to disk. A nil *SourceOrder orders exactly as ComparePos
// does, which is what a caller holding no loaded packages — a test fixture
// checked by hand — gets. Not safe for concurrent use: keys are memoized
// per filename as they are first compared.
type SourceOrder struct {
	roots []moduleRoot // longest directory first, so a nested module wins over its parent
	keys  map[string]sourceKey
}

type moduleRoot struct {
	dir, path string
}

// sourceKey is a filename as SourceOrder ranks it.
type sourceKey struct {
	outside bool   // in no known module: ranked after every module position
	module  string // module path
	file    string // slash path relative to the module root, or the whole slash filename when outside
}

// NewSourceOrder learns every module directory pkgs were loaded from.
func NewSourceOrder(pkgs []*packages.Package) *SourceOrder {
	o := &SourceOrder{keys: map[string]sourceKey{}}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Module == nil || p.Module.Dir == "" {
			continue
		}
		dir := filepath.Clean(p.Module.Dir)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		o.roots = append(o.roots, moduleRoot{dir: dir, path: p.Module.Path})
	}
	sort.Slice(o.roots, func(i, j int) bool {
		if len(o.roots[i].dir) != len(o.roots[j].dir) {
			return len(o.roots[i].dir) > len(o.roots[j].dir)
		}
		return o.roots[i].dir < o.roots[j].dir
	})
	return o
}

// Compare reports whether a sorts before (-1), with (0) or after (1) b.
func (o *SourceOrder) Compare(a, b token.Position) int {
	if o == nil || a.Filename == b.Filename {
		return ComparePos(a, b)
	}
	ka, kb := o.key(a.Filename), o.key(b.Filename)
	switch {
	case ka.outside != kb.outside:
		if kb.outside {
			return -1
		}
		return 1
	case ka.module != kb.module:
		return strings.Compare(ka.module, kb.module)
	case ka.file != kb.file:
		return strings.Compare(ka.file, kb.file)
	}
	// Two spellings of one file — a symlinked directory, say — are still
	// one file; ordering them by the raw names keeps the result total.
	return ComparePos(a, b)
}

func (o *SourceOrder) key(filename string) sourceKey {
	if k, ok := o.keys[filename]; ok {
		return k
	}
	k := sourceKey{outside: true, file: filepath.ToSlash(filename)}
	clean := filepath.Clean(filename)
	for _, r := range o.roots {
		rel, err := filepath.Rel(r.dir, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		k = sourceKey{module: r.path, file: filepath.ToSlash(rel)}
		break
	}
	if o.keys == nil {
		o.keys = map[string]sourceKey{}
	}
	o.keys[filename] = k
	return k
}
