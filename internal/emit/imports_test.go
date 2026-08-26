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

// TestNameAllocatorAvoidsKeywords covers a type whose name lowercases to a
// Go keyword ("Range" -> "range"): allocating it bare would emit `range :=
// ...`, a syntax error. It must be routed through the same numeric-suffix
// path a real name collision uses.
func TestNameAllocatorAvoidsKeywords(t *testing.T) {
	a := NewNameAllocator()
	first := a.Allocate(testNamedFixture("Range"))
	if first == "range" {
		t.Fatalf("Allocate(Range) = %q, want a non-keyword identifier", first)
	}
	if first != "range2" {
		t.Errorf("Allocate(Range) = %q, want range2", first)
	}

	second := a.Allocate(testNamedFixture("Range"))
	if second != "range3" {
		t.Errorf("second Allocate(Range) = %q, want range3", second)
	}

	// A second, unrelated keyword-named type must be seeded independently.
	if got := a.Allocate(testNamedFixture("Type")); got != "type2" {
		t.Errorf("Allocate(Type) = %q, want type2", got)
	}
}

// TestNameAllocatorAvoidsPredeclaredNames covers a type named after a
// predeclared identifier ("New" -> "new", a builtin function; "String" ->
// "string", a builtin type): legal to shadow as a local variable, unlike a
// keyword, but never worth doing in generated code.
func TestNameAllocatorAvoidsPredeclaredNames(t *testing.T) {
	a := NewNameAllocator()
	if got := a.Allocate(testNamedFixture("New")); got != "new2" {
		t.Errorf("Allocate(New) = %q, want new2", got)
	}
	if got := a.Allocate(testNamedFixture("String")); got != "string2" {
		t.Errorf("Allocate(String) = %q, want string2", got)
	}
	// An ordinary type name must still allocate bare, unaffected.
	if got := a.Allocate(testNamedFixture("Widget")); got != "widget" {
		t.Errorf("Allocate(Widget) = %q, want widget", got)
	}
}

// TestImportManagerAliasHandlesKeywordPathSegment reaches the keyword guard
// through Add itself, not just sanitizeIdent directly: a single-segment
// import path falls straight to aliasFor's final fallback
// (sanitizeIdent(strings.Join(segments, "_"))) once its bare package name
// is already claimed by another path.
func TestImportManagerAliasHandlesKeywordPathSegment(t *testing.T) {
	m := NewImportManager()
	m.Add("other/path", "sometypepkg") // claims the bare "sometypepkg" identifier
	got := m.Add("type", "sometypepkg")
	if got != "type_" {
		t.Errorf("Add(%q, %q) = %q, want type_ (a bare keyword alias)", "type", "sometypepkg", got)
	}
}

func TestSanitizeIdentAvoidsKeywords(t *testing.T) {
	cases := []struct{ in, want string }{
		{"type", "type_"},
		{"range", "range_"},
		{"new", "new_"},       // predeclared function, not a keyword
		{"string", "string_"}, // predeclared type, not a keyword
		{"config", "config"},  // not reserved at all: must pass through unchanged
	}
	for _, c := range cases {
		if got := sanitizeIdent(c.in); got != c.want {
			t.Errorf("sanitizeIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
