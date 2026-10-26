package servotest

import (
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/okian/servo/v3/internal/graph"
)

// The fixture types NodeName is checked against. They live in this test
// package so that the type checker below can see them under the same
// import path the test binary compiles them with.
type (
	nnUser                      struct{}
	nnID                        int
	nnStore                     interface{ Get(key string) string }
	nnRepo[T any]               struct{}
	nnPair[K comparable, V any] struct{}
	nnFunc                      func(int) error
)

// Type literals, which have no name for the run time to report, so
// NodeName spells them out itself. Only an interface literal can be a
// node on its own, but its methods can take and return every other kind
// here. Each is an alias, so the type checker sees the literal as it is
// written, and graph.TypeString resolves the alias the way it resolves
// one in a constructor's signature. Parameters are unnamed because the
// run time keeps no names.
type (
	nnSliceOf     = []nnUser
	nnArrayOf     = [3]*nnUser
	nnMapOf       = map[nnID][]string
	nnRecvChanOf  = <-chan nnUser
	nnSendChanOf  = chan<- nnUser
	nnChanOf      = chan nnID
	nnNullaryFunc = func()
	nnVariadic    = func(int, ...nnUser) error
	nnTwoResults  = func(string) (nnUser, error)
	nnEmptyIface  = interface{}
	nnIfaceLit    = interface {
		Close() error
		Get(string) (nnUser, bool)
	}
	nnStructLit = struct {
		A int
		*nnUser
		B string `json:"b"`
	}
)

// Channels of receive-only channels, where go/types writes parentheses Go
// itself needs: chan <-chan T would read as chan<- (chan T). Only a
// bidirectional channel of an unnamed receive-only one takes them, so the
// rest are here too, to show they take none. nnFeedsLit is the shape that
// can be a node: an interface literal whose method returns one.
type (
	nnFeed           <-chan nnUser
	nnChanOfRecvChan = chan (<-chan nnUser)
	nnChanChanOfRecv = chan chan (<-chan nnUser)
	nnSendOfRecvChan = chan<- <-chan nnUser
	nnRecvOfRecvChan = <-chan <-chan nnUser
	nnChanOfFeed     = chan nnFeed
	nnFeedsLit       = interface{ Feeds() chan (<-chan nnUser) }
)

// byte and rune, which run time knows only as uint8 and int32, and which
// servo's keys spell that way too, wherever they are written.
type nnWriterLit = interface{ Write([]byte) (int, error) }

// nnStoreCloser is the cure NodeName's doc comment gives for an interface
// literal that embeds another, which run time sees only flattened: the
// same interface, declared under a name of its own.
type nnStoreCloser interface {
	nnStore
	Close() error
}

// The embedded field is read only through reflection; referenced here so
// the fixture is lint-clean.
var _ = nnStructLit{}.nnUser

// TestNodeNameMatchesTheGenerator checks NodeName against the generator's
// own naming rule, graph.TypeString, rather than against strings written
// down by hand: this package's test files are type-checked, each fixture
// type is built in go/types the way the source spells it, and the two
// renderings have to agree. go/types puts a space after a type argument's
// comma and none inside `struct{}`, where run time does the opposite, and
// the generic cases are where a hand-written expectation would be wrong
// in the same way as the code.
func TestNodeNameMatchesTheGenerator(t *testing.T) {
	scope := typeCheckedTestScope(t)
	named := func(name string) types.Type {
		t.Helper()
		obj := scope.Lookup(name)
		if obj == nil {
			t.Fatalf("fixture type %s not found in the type-checked test package", name)
		}
		return obj.Type()
	}
	instantiate := func(generic string, args ...types.Type) types.Type {
		t.Helper()
		inst, err := types.Instantiate(nil, named(generic), args, true)
		if err != nil {
			t.Fatalf("instantiating %s: %v", generic, err)
		}
		return inst
	}
	user, id := named("nnUser"), named("nnID")
	str := types.Typ[types.String]

	cases := []struct {
		got  string
		want types.Type
	}{
		{NodeName[nnUser](), user},
		{NodeName[*nnUser](), types.NewPointer(user)},
		{NodeName[nnStore](), named("nnStore")},
		{NodeName[nnFunc](), named("nnFunc")},
		{NodeName[nnRepo[nnUser]](), instantiate("nnRepo", user)},
		{NodeName[*nnRepo[*nnUser]](), types.NewPointer(instantiate("nnRepo", types.NewPointer(user)))},
		{NodeName[nnPair[string, []nnUser]](), instantiate("nnPair", str, types.NewSlice(user))},
		{NodeName[*nnPair[nnID, map[string]*nnUser]](), types.NewPointer(instantiate("nnPair", id, types.NewMap(str, types.NewPointer(user))))},
		{NodeName[nnRepo[nnRepo[nnID]]](), instantiate("nnRepo", instantiate("nnRepo", id))},
		{NodeName[nnRepo[nnPair[nnID, nnUser]]](), instantiate("nnRepo", instantiate("nnPair", id, user))},
		{NodeName[nnRepo[any]](), instantiate("nnRepo", types.Universe.Lookup("any").Type())},
		{NodeName[nnRepo[struct{}]](), instantiate("nnRepo", types.NewStruct(nil, nil))},
		{NodeName[nnRepo[nnFunc]](), instantiate("nnRepo", named("nnFunc"))},
		{NodeName[nnRepo[[4]chan<- nnUser]](), instantiate("nnRepo", types.NewArray(types.NewChan(types.SendOnly, user), 4))},
		{NodeName[error](), types.Universe.Lookup("error").Type()},

		// Type literals: here NodeName walks the reflect type itself, so
		// these are the cases where it, not the run time, owes go/types'
		// spacing, its "..." for a variadic parameter, its parentheses
		// round two results, and its quoting of a struct tag.
		{NodeName[nnSliceOf](), named("nnSliceOf")},
		{NodeName[nnArrayOf](), named("nnArrayOf")},
		{NodeName[nnMapOf](), named("nnMapOf")},
		{NodeName[nnRecvChanOf](), named("nnRecvChanOf")},
		{NodeName[nnSendChanOf](), named("nnSendChanOf")},
		{NodeName[nnChanOf](), named("nnChanOf")},
		{NodeName[nnNullaryFunc](), named("nnNullaryFunc")},
		{NodeName[nnVariadic](), named("nnVariadic")},
		{NodeName[nnTwoResults](), named("nnTwoResults")},
		{NodeName[nnEmptyIface](), named("nnEmptyIface")},
		{NodeName[nnIfaceLit](), named("nnIfaceLit")},
		{NodeName[nnStructLit](), named("nnStructLit")},

		{NodeName[nnChanOfRecvChan](), named("nnChanOfRecvChan")},
		{NodeName[nnChanChanOfRecv](), named("nnChanChanOfRecv")},
		{NodeName[nnSendOfRecvChan](), named("nnSendOfRecvChan")},
		{NodeName[nnRecvOfRecvChan](), named("nnRecvOfRecvChan")},
		{NodeName[nnChanOfFeed](), named("nnChanOfFeed")},
		{NodeName[nnFeedsLit](), named("nnFeedsLit")},
		{NodeName[nnRepo[nnChanOfRecvChan]](), instantiate("nnRepo", named("nnChanOfRecvChan"))},
		{NodeName[nnStoreCloser](), named("nnStoreCloser")},
		{NodeName[nnRepo[nnStoreCloser]](), instantiate("nnRepo", named("nnStoreCloser"))},

		{NodeName[nnRepo[[]byte]](), instantiate("nnRepo", types.NewSlice(types.Universe.Lookup("byte").Type()))},
		{NodeName[*nnRepo[rune]](), types.NewPointer(instantiate("nnRepo", types.Universe.Lookup("rune").Type()))},
		{NodeName[nnWriterLit](), named("nnWriterLit")},
	}
	for _, c := range cases {
		if want := graph.TypeString(c.want); c.got != want {
			t.Errorf("NodeName = %q, the generator names it %q", c.got, want)
		}
	}
}

// TestNodeNameIsTheFullImportPath pins the one thing every hand-written
// order assertion got wrong: the short spelling never matches.
func TestNodeNameIsTheFullImportPath(t *testing.T) {
	if got, want := NodeName[*nnUser](), "*github.com/okian/servo/v3/servotest.nnUser"; got != want {
		t.Errorf("NodeName[*nnUser]() = %q, want %q", got, want)
	}
}

// typeCheckedTestScope type-checks this package with its test files, the
// way the generator type-checks a module, and returns the package scope.
func typeCheckedTestScope(t *testing.T) *types.Scope {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo, Tests: true}
	pkgs, err := packages.Load(cfg, "github.com/okian/servo/v3/servotest")
	if err != nil {
		t.Fatalf("loading servotest: %v", err)
	}
	for _, p := range pkgs {
		// The internal test variant: the package's own path, with its test
		// files compiled in — the ID names the test binary it belongs to.
		if p.PkgPath == "github.com/okian/servo/v3/servotest" && strings.Contains(p.ID, ".test]") && p.Types != nil {
			if len(p.Errors) > 0 {
				t.Fatalf("type-checking servotest's tests: %v", p.Errors)
			}
			return p.Types.Scope()
		}
	}
	t.Fatal("go/packages returned no internal test variant of servotest")
	return nil
}
