package graph

import (
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/packages"
)

func modulePkg(dir, path string) *packages.Package {
	return &packages.Package{PkgPath: path, Module: &packages.Module{Path: path, Dir: dir}}
}

// TestSourceOrderRanksByModuleNotDirectory: the order of two declarations
// in different modules is decided by the modules' paths, so moving one
// module's checkout from ../a-shared to ../z-shared — which swapped two
// trace wrappers in a committed file — changes nothing.
func TestSourceOrderRanksByModuleNotDirectory(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "m-app")
	for _, shared := range []string{filepath.Join(root, "a-shared"), filepath.Join(root, "z-shared")} {
		o := NewSourceOrder([]*packages.Package{modulePkg(app, "example.com/app"), modulePkg(shared, "example.com/shared")})
		inApp := token.Position{Filename: filepath.Join(app, "cmd", "app", "spec.go"), Line: 14, Column: 3}
		inShared := token.Position{Filename: filepath.Join(shared, "wiring", "wiring.go"), Line: 11, Column: 3}
		if got := o.Compare(inApp, inShared); got >= 0 {
			t.Errorf("shared module at %s: Compare(app, shared) = %d, want < 0 — example.com/app sorts before example.com/shared wherever either lives", shared, got)
		}
		if got := o.Compare(inShared, inApp); got <= 0 {
			t.Errorf("shared module at %s: Compare(shared, app) = %d, want > 0", shared, got)
		}
	}
}

func TestSourceOrderWithinAModule(t *testing.T) {
	root := t.TempDir()
	o := NewSourceOrder([]*packages.Package{modulePkg(root, "example.com/app")})
	at := func(rel string, line, col int) token.Position {
		return token.Position{Filename: filepath.Join(root, filepath.FromSlash(rel)), Line: line, Column: col}
	}
	got := []token.Position{at("b/b.go", 1, 1), at("a/a.go", 9, 1), at("a/a.go", 3, 7), at("a/a.go", 3, 2), at("a.go", 5, 1)}
	sort.Slice(got, func(i, j int) bool { return o.Compare(got[i], got[j]) < 0 })
	want := []token.Position{at("a.go", 5, 1), at("a/a.go", 3, 2), at("a/a.go", 3, 7), at("a/a.go", 9, 1), at("b/b.go", 1, 1)}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted = %v, want %v", got, want)
		}
	}
}

// A nested module is its own module: its files rank under its own path,
// not as a subdirectory of the module around it.
func TestSourceOrderPrefersTheNestedModule(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	o := NewSourceOrder([]*packages.Package{modulePkg(root, "example.com/zmono"), modulePkg(tools, "example.com/atools")})
	inTools := token.Position{Filename: filepath.Join(tools, "clock", "clock.go"), Line: 1}
	inMono := token.Position{Filename: filepath.Join(root, "aaa", "a.go"), Line: 1}
	if got := o.Compare(inTools, inMono); got >= 0 {
		t.Errorf("Compare(nested, outer) = %d, want < 0: the nested file ranks as example.com/atools, not example.com/zmono/tools", got)
	}
}

// A position in no known module — the standard library, read from export
// data — sorts after every module position, and a nil order is ComparePos.
func TestSourceOrderOutsideModulesAndNil(t *testing.T) {
	root := t.TempDir()
	o := NewSourceOrder([]*packages.Package{modulePkg(root, "example.com/app"), {PkgPath: "fmt"}})
	inApp := token.Position{Filename: filepath.Join(root, "z.go"), Line: 1}
	outside := token.Position{Filename: "/aaa/goroot/src/fmt/print.go", Line: 1}
	if got := o.Compare(inApp, outside); got >= 0 {
		t.Errorf("Compare(module, outside) = %d, want < 0", got)
	}
	var none *SourceOrder
	a, b := token.Position{Filename: "/b.go", Line: 1}, token.Position{Filename: "/a.go", Line: 9}
	if got, want := none.Compare(a, b), ComparePos(a, b); got != want {
		t.Errorf("nil order: Compare = %d, ComparePos = %d", got, want)
	}
}

// Two module directories of the same length still rank by module path,
// whichever order the packages arrive in, and a position outside every
// module sorts after a module position from either side of the compare.
func TestSourceOrderTiesAndOutsiders(t *testing.T) {
	root := t.TempDir()
	aaa, bbb := filepath.Join(root, "aaa"), filepath.Join(root, "bbb")
	inAAA := token.Position{Filename: filepath.Join(aaa, "a.go"), Line: 1}
	inBBB := token.Position{Filename: filepath.Join(bbb, "a.go"), Line: 1}
	outside := token.Position{Filename: "/aaa/goroot/src/fmt/print.go", Line: 1}
	for _, pkgs := range [][]*packages.Package{
		{modulePkg(aaa, "example.com/zeta"), modulePkg(bbb, "example.com/alpha")},
		{modulePkg(bbb, "example.com/alpha"), modulePkg(aaa, "example.com/zeta")},
	} {
		o := NewSourceOrder(pkgs)
		if got := o.Compare(inBBB, inAAA); got >= 0 {
			t.Errorf("Compare(alpha's file, zeta's file) = %d, want < 0: example.com/alpha sorts first wherever its directory sorts", got)
		}
		if got := o.Compare(outside, inAAA); got <= 0 {
			t.Errorf("Compare(outside, module) = %d, want > 0", got)
		}
	}
}

// Two spellings of one file — reached through a symlinked or unclean
// directory — are the same file to SourceOrder, so their raw names decide,
// which keeps the order total.
func TestSourceOrderTwoSpellingsOfOneFile(t *testing.T) {
	root := t.TempDir()
	o := NewSourceOrder([]*packages.Package{modulePkg(root, "example.com/app")})
	clean := token.Position{Filename: filepath.Join(root, "a.go"), Line: 9}
	unclean := token.Position{Filename: root + string(filepath.Separator) + "." + string(filepath.Separator) + "a.go", Line: 2}
	if got, want := o.Compare(clean, unclean), ComparePos(clean, unclean); got != want {
		t.Errorf("Compare = %d, want ComparePos's %d", got, want)
	}
	if o.Compare(clean, unclean) != -o.Compare(unclean, clean) {
		t.Error("Compare is not antisymmetric for two spellings of one file")
	}
}

// The zero SourceOrder — written as a literal rather than built by
// NewSourceOrder — knows no module, so every file is outside one and they
// order by slash path; it fills in its memo on first use instead of
// writing to a nil map.
func TestSourceOrderZeroValue(t *testing.T) {
	var o SourceOrder
	a, b := token.Position{Filename: "/a/z.go", Line: 9}, token.Position{Filename: "/b/a.go", Line: 1}
	if got := o.Compare(a, b); got >= 0 {
		t.Errorf("Compare(/a/z.go, /b/a.go) = %d, want < 0", got)
	}
	if got := o.Compare(b, a); got <= 0 {
		t.Errorf("Compare(/b/a.go, /a/z.go) = %d, want > 0", got)
	}
}
