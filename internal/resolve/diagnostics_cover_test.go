package resolve

import (
	"strings"
	"testing"
)

// getterSrc is a package declaring one implementation of a Get() string
// interface, under whatever package name and type name it is given.
func getterSrc(pkg, typ string) string {
	return "package " + pkg + "\n\ntype " + typ + " struct{}\n\nfunc (*" + typ + ") Get() string { return \"\" }\n\nfunc New" + typ + "() *" + typ + " { return &" + typ + "{} }\n"
}

// onlyDiagnostic resolves in and returns its one diagnostic, failing the
// test when there is not exactly one.
func onlyDiagnostic(t *testing.T, in Input) Diagnostic {
	t.Helper()
	_, diags := Resolve(in)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want exactly 1:\n%s", len(diags), diagText(diags))
	}
	return diags[0]
}

// spellingSpecSrc is the injector package, and its one file is the spec
// file: it imports one implementation's package plainly, one under a name
// of its own, one with a dot, and one only for its side effects.
const spellingSpecSrc = `package app

import (
	. "example.com/mod/kinds"
	"example.com/mod/a/store"
	cache "example.com/mod/lru"
	_ "example.com/mod/side"
)

var _ = File{}
var _ = store.Mem{}
var _ = cache.LRU{}

type Store interface{ Get() string }

type Server struct{}

func NewServer(s Store) *Server { return &Server{} }
`

// TestAmbiguitySpellsTypesTheWayTheSpecFileImportsThem: a suggested Bind line
// is only paste-ready if it uses the names the spec file already gives
// each package — its own name, an alias, nothing at all for a dot import
// or for the injector's own package — and, for a package the file does
// not import yet, a name that does not collide with one it does, listed
// with the import to add.
func TestAmbiguitySpellsTypesTheWayTheSpecFileImportsThem(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.com/mod/a/store", getterSrc("store", "Mem")},
		pkgSrc{"example.com/mod/b/store", getterSrc("store", "Disk")},
		pkgSrc{"example.com/mod/kinds", getterSrc("kinds", "File")},
		pkgSrc{"example.com/mod/lru", getterSrc("lru", "LRU")},
		pkgSrc{"example.com/mod/side", "package side\n"},
		pkgSrc{"example.com/mod/app", spellingSpecSrc})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.File = m.pkg("example.com/mod/app").Syntax[0]

	msg := onlyDiagnostic(t, in).Message
	var at []int
	for _, want := range []string{
		"4 types in this module implement Store — pick one with servo.Bind:\n",
		"servo.Bind[Store, *store.Mem]()",
		// b/store's package is also named store, which the spec file
		// already spends on a/store.
		"servo.Bind[Store, *bstore.Disk]()",
		"servo.Bind[Store, *File]()",
		"servo.Bind[Store, *cache.LRU]()",
	} {
		i := strings.Index(msg, want)
		if i < 0 {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
		at = append(at, i)
	}
	for i := 1; i < len(at); i++ {
		if at[i] < at[i-1] {
			t.Errorf("candidates are not listed in source order:\n%s", msg)
			break
		}
	}
	if want := "  with these imports in the spec file:\n      bstore \"example.com/mod/b/store\"\n"; !strings.HasSuffix(msg, want) {
		t.Errorf("diagnostic does not end with exactly the one import to add, %q:\n%s", want, msg)
	}
}

// TestAmbiguityAmongTheInjectorsOwnTypesNeedsNoImports: types in the
// injector's own package are written bare, and a suggestion that needs no
// new import says nothing about imports at all.
func TestAmbiguityAmongTheInjectorsOwnTypesNeedsNoImports(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", `package app

type Store interface{ Get() string }

type Mem struct{}

func (*Mem) Get() string { return "" }
func NewMem() *Mem       { return &Mem{} }

type Disk struct{}

func (*Disk) Get() string { return "" }
func NewDisk() *Disk      { return &Disk{} }

type Server struct{}

func NewServer(s Store) *Server { return &Server{} }
`})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))
	in.Spec.File = m.pkg("example.com/mod/app").Syntax[0]

	msg := mustFail(t, in)
	for _, want := range []string{
		"2 types in this module implement Store — pick one with servo.Bind:\n",
		"servo.Bind[Store, *Mem]()",
		"servo.Bind[Store, *Disk]()",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "with these imports") {
		t.Errorf("diagnostic lists imports although every type is the injector's own:\n%s", msg)
	}
}

// TestASingleElementImportPathGetsANumberedAlias: the standard library's
// sync has no parent path element to borrow when the spec file already
// spends the name on a package of the module's own, so the suggestion
// aliases it with a numbered name rather than writing one that would not
// compile next to the existing import.
func TestASingleElementImportPathGetsANumberedAlias(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.com/mod/sync", "package sync\n\ntype Job struct{}\n"},
		pkgSrc{"example.com/mod/locks", `package locks

type Spin struct{}

func (*Spin) Lock()   {}
func (*Spin) Unlock() {}
func NewSpin() *Spin  { return &Spin{} }

type Ticket struct{}

func (*Ticket) Lock()    {}
func (*Ticket) Unlock()  {}
func NewTicket() *Ticket { return &Ticket{} }
`},
		pkgSrc{"example.com/mod/guard", `package guard

import "sync"

type Guard struct{}

func NewGuard(l sync.Locker) *Guard { return &Guard{} }
`},
		pkgSrc{"example.com/mod/app", "package app\n\nimport \"example.com/mod/sync\"\n\nvar _ = sync.Job{}\n"})
	in := m.input("example.com/mod/app", m.ptr("example.com/mod/guard", "Guard"))
	in.Spec.File = m.pkg("example.com/mod/app").Syntax[0]

	msg := onlyDiagnostic(t, in).Message
	for _, want := range []string{
		"2 types in this module implement pkg1.Locker — pick one with servo.Bind:\n",
		"servo.Bind[pkg1.Locker, *locks.Spin]()",
		"servo.Bind[pkg1.Locker, *locks.Ticket]()",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if want := "  with these imports in the spec file:\n      \"example.com/mod/locks\"\n      pkg1 \"sync\"\n"; !strings.HasSuffix(msg, want) {
		t.Errorf("diagnostic does not end with the two imports to add, sorted, %q:\n%s", want, msg)
	}
}

// TestForeignProvidersOfAnUnnamedTypeGetAPlaceholderName: the advice to
// write a provider of one's own names it after the contested type. An
// unnamed interface has no name to borrow, so the suggested constructor
// is NewValue rather than a bare New that says nothing about what it
// builds.
func TestForeignProvidersOfAnUnnamedTypeGetAPlaceholderName(t *testing.T) {
	m := checkModule(t,
		pkgSrc{"example.org/lib", `package lib

func Primary() interface{ Get() string } { return nil }
func Replica() interface{ Get() string } { return nil }
`},
		pkgSrc{"example.com/mod/app", `package app

type Server struct{}

func NewServer(g interface{ Get() string }) *Server { return &Server{} }
`})
	msg := onlyDiagnostic(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", "Server"))).Message
	for _, want := range []string{
		"servo: ambiguous provider for interface{Get() string}\n",
		"2 functions outside this module produce interface{Get() string}",
		"lib.Primary ", "lib.Replica ",
		"\tfunc NewValue(...) interface{Get() string}\n",
		"servo.Value[interface{Get() string}]()",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("diagnostic missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "with these imports") {
		t.Errorf("diagnostic lists imports for a type that needs none:\n%s", msg)
	}
}
