package load

import (
	"go/token"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
)

// TestPositionsString pins the three forms the generated file and `servo
// graph` write — and the inputs they leave alone — because the two
// producers have to agree with each other byte for byte, and `servo check`
// has to agree with a file generated on another machine.
func TestPositionsString(t *testing.T) {
	sep := string(filepath.Separator)
	modRoot := filepath.Join(sep, "mod", "root")
	depDir := filepath.Join(sep, "home", "me", "go", "pkg", "mod", "github.com", "acme", "dep@v1.2.0")
	goroot := filepath.Join(sep, "usr", "local", "go")
	cache := filepath.Join(sep, "home", "me", ".cache", "go-build", "ab", "cd")
	pkgs := []*packages.Package{
		{
			PkgPath: "example.com/app/internal/store",
			Module:  &packages.Module{Path: "example.com/app", Main: true, Dir: modRoot},
			GoFiles: []string{filepath.Join(modRoot, "internal", "store", "store.go")},
		},
		{
			PkgPath: "github.com/acme/dep/sub",
			Module:  &packages.Module{Path: "github.com/acme/dep", Dir: depDir},
			GoFiles: []string{filepath.Join(depDir, "sub", "dep.go")},
		},
		{
			PkgPath: "net/http",
			GoFiles: []string{filepath.Join(goroot, "src", "net", "http", "server.go")},
		},
		{
			PkgPath:         "os/user",
			GoFiles:         []string{filepath.Join(goroot, "src", "os", "user", "lookup.go")},
			CompiledGoFiles: []string{filepath.Join(cache, "lookup_unix.cgo1.go")},
		},
		{
			// -mod=vendor: the dependency's files sit under the module
			// root, and its module has no directory of its own.
			PkgPath: "github.com/acme/vendored",
			Module:  &packages.Module{Path: "github.com/acme/vendored", Version: "v1.0.0"},
			GoFiles: []string{filepath.Join(modRoot, "vendor", "github.com", "acme", "vendored", "v.go")},
		},
		{
			// A nested module the main module replaces to ./tools.
			PkgPath: "example.com/app/tools/gen",
			Module:  &packages.Module{Path: "example.com/app/tools", Dir: filepath.Join(modRoot, "tools")},
			GoFiles: []string{filepath.Join(modRoot, "tools", "gen", "gen.go")},
		},
	}
	p := NewPositions(modRoot, pkgs)

	cases := []struct {
		name string
		pos  token.Position
		want string
	}{
		{
			name: "inside the module: relative to the module root",
			pos:  token.Position{Filename: filepath.Join(modRoot, "internal", "store", "store.go"), Line: 12, Column: 6},
			want: "internal/store/store.go:12:6",
		},
		{
			// A provider in a dependency lives in the module cache, or in
			// whatever directory a replace directive points at. Neither is a
			// path another machine has, so the file is named by the one
			// thing every machine agrees on: its package's import path.
			name: "in another module: the package's import path and the file name",
			pos:  token.Position{Filename: filepath.Join(depDir, "sub", "dep.go"), Line: 3, Column: 1},
			want: "github.com/acme/dep/sub/dep.go:3:1",
		},
		{
			name: "in the standard library: $GOROOT/src",
			pos:  token.Position{Filename: filepath.Join(goroot, "src", "net", "http", "server.go"), Line: 3020, Column: 6},
			want: "$GOROOT/src/net/http/server.go:3020:6",
		},
		{
			name: "a cgo-processed source in the build cache is attributed to its package",
			pos:  token.Position{Filename: filepath.Join(cache, "lookup_unix.cgo1.go"), Line: 9, Column: 6},
			want: "$GOROOT/src/os/user/lookup_unix.cgo1.go:9:6",
		},
		{
			// It sits inside the module root, but it is another module's
			// file: a checkout that vendors and one that does not have to
			// write the same position for it.
			name: "a vendored dependency: its import path, not vendor/",
			pos:  token.Position{Filename: filepath.Join(modRoot, "vendor", "github.com", "acme", "vendored", "v.go"), Line: 4, Column: 6},
			want: "github.com/acme/vendored/v.go:4:6",
		},
		{
			name: "a nested module inside the root: its import path",
			pos:  token.Position{Filename: filepath.Join(modRoot, "tools", "gen", "gen.go"), Line: 7, Column: 6},
			want: "example.com/app/tools/gen/gen.go:7:6",
		},
		{
			name: "a file no loaded package claims is left alone",
			pos:  token.Position{Filename: filepath.Join(sep, "elsewhere", "x.go"), Line: 1, Column: 1},
			want: filepath.Join(sep, "elsewhere", "x.go") + ":1:1",
		},
		{
			// filepath.Rel cannot relate a relative path to an absolute
			// root, and the position is already in the form the generated
			// file writes.
			name: "an already-relative position is passed through",
			pos:  token.Position{Filename: "internal/store/store.go", Line: 12, Column: 6},
			want: "internal/store/store.go:12:6",
		},
		{
			name: "a sibling directory whose name starts with the root is not inside it",
			pos:  token.Position{Filename: modRoot + "2" + sep + "x.go", Line: 1, Column: 1},
			want: modRoot + "2" + sep + "x.go:1:1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.String(c.pos); got != c.want {
				t.Errorf("String(%q) = %q, want %q", c.pos, got, c.want)
			}
		})
	}
}

// TestPositionsWithoutAModuleLeaveThePositionAlone covers the two shapes a
// hand-built test fixture produces: no Positions at all, and one with no
// module root. Inventing a root would rewrite positions against a
// directory that does not exist.
func TestPositionsWithoutAModuleLeaveThePositionAlone(t *testing.T) {
	pos := token.Position{Filename: filepath.Join(string(filepath.Separator), "mod", "root", "store.go"), Line: 12, Column: 6}
	want := pos.String()
	var nilPositions *Positions
	if got := nilPositions.String(pos); got != want {
		t.Errorf("nil Positions: got %q, want %q", got, want)
	}
	if got := NewPositions("", nil).String(pos); got != want {
		t.Errorf("empty module root: got %q, want %q", got, want)
	}
}

// TestLoadedPositionsAreRelativeToTheInjectorsModule covers both answers
// Loaded.Positions can give for the module root. The injector's module
// directory is what every position `servo graph` prints is trimmed
// against, which is the only reason those strings match the ones the
// generated App.Graph() carries. An injector package that came back
// without module information has no such prefix, and inventing one would
// trim a path that was never there and mangle every position.
func TestLoadedPositionsAreRelativeToTheInjectorsModule(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "src", "app")
	pos := token.Position{Filename: filepath.Join(root, "api", "api.go"), Line: 15, Column: 6}
	l := &Loaded{}

	withModule := &packages.Package{Module: &packages.Module{Dir: root}}
	if got, want := l.Positions(withModule).String(pos), "api/api.go:15:6"; got != want {
		t.Errorf("with module info: got %q, want %q", got, want)
	}

	withoutModule := &packages.Package{}
	if got, want := l.Positions(withoutModule).String(pos), pos.String(); got != want {
		t.Errorf("without module info: got %q, want %q (nothing trimmed)", got, want)
	}
}
