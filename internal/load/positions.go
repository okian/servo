package load

import (
	"go/token"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Positions renders source positions the way the generated file commits
// them and `servo graph` prints them: machine-independent, so two
// checkouts at different absolute paths — or `servo check` on a machine
// other than the one that ran `servo generate` — agree byte for byte.
//
// Three forms, by where the file lives:
//
//   - Inside the injector's module: relative to the module root,
//     "api/api.go:15:6".
//   - In another module, whether a dependency or a sibling the main module
//     replaces to: the declaring package's import path plus the file name,
//     "github.com/okian/servo/v3/middleware/recover.go:17:6". The module
//     cache or replace directory would embed where this machine keeps the
//     dependency, which is the one thing a committed file must never do.
//   - In the standard library: "$GOROOT/src/net/http/server.go:3020:6",
//     for the same reason.
//
// A file no loaded package claims is left as it came, and so is a position
// that is already relative: the generated file and the graph command are
// the only producers, and these are the only forms they write.
type Positions struct {
	// ModRoot is the injector module's directory, or "" for a Spec built
	// by hand in a test, which has no module to be relative to.
	ModRoot string
	// byDir maps each loaded package's source directories to the package,
	// which is how String tells which module a file belongs to.
	byDir map[string]*packages.Package
}

// NewPositions indexes pkgs for String. pkgs may be nil, in which case
// only the module-relative form applies.
func NewPositions(modRoot string, pkgs []*packages.Package) *Positions {
	p := &Positions{ModRoot: modRoot, byDir: make(map[string]*packages.Package)}
	for _, pkg := range pkgs {
		// CompiledGoFiles too: a cgo package's type-checked sources sit in
		// the build cache, not beside its GoFiles.
		for _, files := range [][]string{pkg.GoFiles, pkg.CompiledGoFiles} {
			for _, f := range files {
				dir := filepath.Dir(f)
				if _, ok := p.byDir[dir]; !ok {
					p.byDir[dir] = pkg
				}
			}
		}
	}
	return p
}

// Positions returns the renderer for an injector in pkg's module, with
// every loaded package indexed.
func (l *Loaded) Positions(injector *packages.Package) *Positions {
	modRoot := ""
	if injector != nil && injector.Module != nil {
		modRoot = injector.Module.Dir
	}
	return NewPositions(modRoot, l.All)
}

// String renders pos by the rules above. A nil *Positions renders it
// unchanged.
func (p *Positions) String(pos token.Position) string {
	if p == nil || pos.Filename == "" || !filepath.IsAbs(pos.Filename) {
		return pos.String()
	}
	if p.ModRoot != "" {
		if rel, err := filepath.Rel(p.ModRoot, pos.Filename); err == nil && !escapes(rel) {
			pos.Filename = filepath.ToSlash(rel)
			return pos.String()
		}
	}
	pkg := p.byDir[filepath.Dir(pos.Filename)]
	if pkg == nil {
		return pos.String()
	}
	base := filepath.Base(pos.Filename)
	if pkg.Module == nil {
		pos.Filename = path.Join("$GOROOT/src", pkg.PkgPath, base)
	} else {
		pos.Filename = path.Join(pkg.PkgPath, base)
	}
	return pos.String()
}

// escapes reports whether a path returned by filepath.Rel leads out of
// the directory it is relative to.
func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
