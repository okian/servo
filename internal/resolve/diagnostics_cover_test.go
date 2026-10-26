package resolve

import (
	"go/ast"
	"go/parser"
	"go/types"
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

// duplicateAdviceSrc gives each shape a result type can take two
// constructors and one consumer, so resolving a consumer is an ambiguity
// whose advice has to make up a type to hold the contested one.
const duplicateAdviceSrc = `package app

type Logger struct{}

func NewLogger() *Logger    { return &Logger{} }
func NewLoggerTwo() *Logger { return &Logger{} }

type Config struct{}

func NewConfig() Config    { return Config{} }
func NewConfigTwo() Config { return Config{} }

type Cache[T any] struct{}

func NewCache() *Cache[string]    { return nil }
func NewCacheTwo() *Cache[string] { return nil }

type Store interface{ Get() string }

func NewStore() Store    { return nil }
func NewStoreTwo() Store { return nil }

func NewStorePtr() *Store    { return nil }
func NewStorePtrTwo() *Store { return nil }

type LoggerRef *Logger

func NewLoggerRef() LoggerRef    { return nil }
func NewLoggerRefTwo() LoggerRef { return nil }

func NewGetter() interface{ Get() string }    { return nil }
func NewGetterTwo() interface{ Get() string } { return nil }

type ForLogger struct{}
type ForConfig struct{}
type ForCache struct{}
type ForStore struct{}
type ForStorePtr struct{}
type ForLoggerRef struct{}
type ForGetter struct{}

func NewForLogger(v *Logger) *ForLogger                   { return nil }
func NewForConfig(v Config) *ForConfig                    { return nil }
func NewForCache(v *Cache[string]) *ForCache              { return nil }
func NewForStore(v Store) *ForStore                       { return nil }
func NewForStorePtr(v *Store) *ForStorePtr                { return nil }
func NewForLoggerRef(v LoggerRef) *ForLoggerRef           { return nil }
func NewForGetter(v interface{ Get() string }) *ForGetter { return nil }
`

// TestDuplicateProviderAdviceIsGoForEveryResultType: the advice for two
// constructors of one type is a declaration to paste, so it has to be Go.
// A type Go can embed is embedded as before, keeping its methods. One it
// cannot (an unnamed interface, a pointer to an interface, a named pointer
// type) used to be written embedded anyway, which does not parse or does
// not compile; it is held in a field named the way an embedded one would
// be. Each declaration is parsed, and type-checked beside the package it
// names the types of.
func TestDuplicateProviderAdviceIsGoForEveryResultType(t *testing.T) {
	m := checkModule(t, pkgSrc{"example.com/mod/app", duplicateAdviceSrc})
	for _, tc := range []struct{ consumer, advice string }{
		{"ForLogger", "type PrimaryLogger struct{ *Logger }"},
		{"ForConfig", "type PrimaryConfig struct{ Config }"},
		{"ForCache", "type PrimaryCache struct{ *Cache[string] }"},
		{"ForStore", "type PrimaryStore struct{ Store }"},
		{"ForStorePtr", "type PrimaryStore struct{ Store *Store }"},
		{"ForLoggerRef", "type PrimaryLoggerRef struct{ LoggerRef LoggerRef }"},
		{"ForGetter", "type PrimaryValue struct{ Value interface{Get() string} }"},
	} {
		t.Run(tc.consumer, func(t *testing.T) {
			msg := onlyDiagnostic(t, m.input("example.com/mod/app", m.ptr("example.com/mod/app", tc.consumer))).Message
			var advice string
			for _, line := range strings.Split(msg, "\n") {
				if s, ok := strings.CutPrefix(line, "\ttype Primary"); ok {
					advice = "type Primary" + s
				}
			}
			if advice != tc.advice {
				t.Errorf("advice = %q, want %q:\n%s", advice, tc.advice, msg)
			}
			f, err := parser.ParseFile(m.fset, "advice.go", "package app\n\n"+advice+"\n", 0)
			if err != nil {
				t.Fatalf("advice %q does not parse: %v", advice, err)
			}
			files := append(append([]*ast.File(nil), m.pkg("example.com/mod/app").Syntax...), f)
			if _, err := (&types.Config{Importer: importerFor(loadPkg(t, "context"))}).Check("example.com/mod/app", m.fset, files, nil); err != nil {
				t.Errorf("advice %q does not compile in its package: %v", advice, err)
			}
		})
	}
}
