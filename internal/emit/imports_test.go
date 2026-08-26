package emit

import (
	"go/types"
	"strings"
	"testing"
)

// testNamedFixture builds a bare *types.Named for name, without needing a
// real source file — enough for NameAllocator, which only looks at the
// type's own declared name.
func testNamedFixture(name string) types.Type {
	pkg := types.NewPackage("example.com/x", "x")
	obj := types.NewTypeName(0, pkg, name, nil)
	return types.NewNamed(obj, types.NewStruct(nil, nil), nil)
}

func TestImportManagerNoCollision(t *testing.T) {
	m := NewImportManager()
	if got := m.Add("example.com/foo/store", "store"); got != "store" {
		t.Errorf("got %q, want store", got)
	}
	// Re-adding the same path must return the same identifier.
	if got := m.Add("example.com/foo/store", "store"); got != "store" {
		t.Errorf("re-add got %q, want store", got)
	}
}

func TestImportManagerAliasesByPathSegmentOnCollision(t *testing.T) {
	m := NewImportManager()
	first := m.Add("example.com/foo/config", "config")
	second := m.Add("example.com/bar/config", "config")

	if first != "config" {
		t.Errorf("first = %q, want config (unaliased)", first)
	}
	if second != "barconfig" {
		t.Errorf("second = %q, want barconfig (path-segment alias, not a numeric suffix)", second)
	}
	if strings.ContainsAny(second, "0123456789") {
		t.Errorf("second = %q must not contain a bare numeric suffix", second)
	}
}

func TestImportManagerThreeWayCollisionExtendsFurther(t *testing.T) {
	m := NewImportManager()
	m.Add("example.com/foo/config", "config")
	m.Add("example.com/bar/config", "config")
	third := m.Add("example.com/bar/sub/config", "config")
	if third == "config" || third == "barconfig" {
		t.Errorf("third = %q, want a further-disambiguated alias distinct from the first two", third)
	}
}

func TestImportManagerRenderImportsSortedByPath(t *testing.T) {
	m := NewImportManager()
	m.Add("example.com/zzz", "zzz")
	m.Add("example.com/aaa", "aaa")
	out := m.RenderImports()
	if strings.Index(out, `"example.com/aaa"`) > strings.Index(out, `"example.com/zzz"`) {
		t.Errorf("imports not sorted by path:\n%s", out)
	}
}

func TestNameAllocatorDedupesReadably(t *testing.T) {
	a := NewNameAllocator()
	names := []string{a.Allocate(testNamedFixture("Pool")), a.Allocate(testNamedFixture("Pool")), a.Allocate(testNamedFixture("Pool"))}
	want := []string{"pool", "pool2", "pool3"}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}
