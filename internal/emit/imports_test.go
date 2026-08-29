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

// TestImportManagerReAddAfterAliasingReturnsCachedAlias covers Add's
// already-registered-with-a-non-empty-alias branch: once a path has been
// aliased by a collision, registering the exact same path again (a second
// type from the same already-colliding package) must return that same
// alias from the cache, not recompute or drop it.
func TestImportManagerReAddAfterAliasingReturnsCachedAlias(t *testing.T) {
	m := NewImportManager()
	m.Add("example.com/foo/config", "config")
	first := m.Add("example.com/bar/config", "config")
	if first != "barconfig" {
		t.Fatalf("first = %q, want barconfig", first)
	}
	second := m.Add("example.com/bar/config", "config")
	if second != "barconfig" {
		t.Errorf("re-add of an already-aliased path = %q, want the cached alias barconfig", second)
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

// TestSanitizeIdentStripsDotsAndDashes covers aliasFor's real motivation:
// path segments like "gopkg.in" or "x-tools" contain characters that are
// legal in an import path but not in a Go identifier.
func TestSanitizeIdentStripsDotsAndDashes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gopkg.in", "gopkgin"},
		{"x-tools", "xtools"},
		{"3rdparty", "_3rdparty"}, // a leading digit is not a legal identifier start
		{"..--", "pkg"},           // nothing survives stripping: falls back to a fixed name
	}
	for _, c := range cases {
		if got := sanitizeIdent(c.in); got != c.want {
			t.Errorf("sanitizeIdent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestImportManagerRenderImportsIncludesAliasForm covers RenderImports'
// aliased-line branch: TestImportManagerRenderImportsSortedByPath only
// exercises unaliased imports, so the "alias \"path\"" rendering (as
// opposed to a bare "path") was never reached.
func TestImportManagerRenderImportsIncludesAliasForm(t *testing.T) {
	m := NewImportManager()
	m.Add("example.com/foo/config", "config")
	m.Add("example.com/bar/config", "config") // collides -> aliased to barconfig
	out := m.RenderImports()
	if !strings.Contains(out, `barconfig "example.com/bar/config"`) {
		t.Errorf("expected an aliased import line, got:\n%s", out)
	}
}

// TestImportManagerAliasesASingleSegmentPathWithANumericSuffix is the
// case extending backward cannot reach: "errors" has no earlier segment
// to borrow, and its own name is already spoken for by a user package
// scanned first. Falling through used to return "errors" a second time,
// so the file declared two imports under one identifier and did not
// compile — with `servo generate` reporting success, since it formats the
// output rather than type-checking it.
func TestImportManagerAliasesASingleSegmentPathWithANumericSuffix(t *testing.T) {
	m := NewImportManager()
	if got := m.Add("example.com/app/errors", "errors"); got != "errors" {
		t.Fatalf("the user package = %q, want the bare errors it claimed first", got)
	}
	got := m.Add("errors", "errors")
	if got == "errors" {
		t.Fatalf("the stdlib import reused the identifier the user package holds")
	}
	if got != "errors2" {
		t.Errorf("Add(\"errors\", \"errors\") = %q, want errors2", got)
	}

	// The rendered block is where the collision would have shown up: two
	// lines, two distinct identifiers.
	out := m.RenderImports()
	if !strings.Contains(out, "errors2 \"errors\"") {
		t.Errorf("the stdlib import is not rendered under its alias:\n%s", out)
	}
	if !strings.Contains(out, "\t\"example.com/app/errors\"\n") {
		t.Errorf("the user package lost its unaliased line:\n%s", out)
	}
}

// TestImportManagerAliasesAPathWhoseEveryPrefixIsTaken is the other way
// aliasFor runs out: every backward extension of foo/bar/config is
// already claimed, and so is the underscore-joined whole path. There is
// no segment left to say anything with, so a numeric suffix is all that
// remains — worse than "barconfig" at telling a reader which config this
// is, and still better than an identifier that names two imports.
func TestImportManagerAliasesAPathWhoseEveryPrefixIsTaken(t *testing.T) {
	m := NewImportManager()
	m.Reserve("config", "barconfig", "foobarconfig", "foo_bar_config")

	got := m.Add("foo/bar/config", "config")
	for _, taken := range []string{"config", "barconfig", "foobarconfig", "foo_bar_config"} {
		if got == taken {
			t.Fatalf("Add returned %q, which is already claimed", got)
		}
	}
	if got != "foo_bar_config2" {
		t.Errorf("Add(%q, %q) = %q, want foo_bar_config2", "foo/bar/config", "config", got)
	}
}
