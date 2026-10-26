package emit

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// TestBaseNameFallsBackToVForUnnamedType covers unwrapToNamed's default
// case (neither *types.Named nor *types.Pointer): a provider returning an
// anonymous interface directly has no declared name at all to derive an
// identifier from, so baseName falls back to the generic "v".
func TestBaseNameFallsBackToVForUnnamedType(t *testing.T) {
	iface := types.NewInterfaceType(nil, nil)
	iface.Complete()
	if got := baseName(iface); got != "v" {
		t.Errorf("baseName(anonymous interface) = %q, want v", got)
	}
}

func TestLowerFirstEmptyString(t *testing.T) {
	if got := lowerFirst(""); got != "" {
		t.Errorf("lowerFirst(\"\") = %q, want empty", got)
	}
}

func TestCapitalizeEmptyString(t *testing.T) {
	if got := capitalize(""); got != "" {
		t.Errorf("capitalize(\"\") = %q, want empty", got)
	}
}

// namedType is a minimal *types.Named for exercising the name allocators
// without standing up a whole package.
func namedType(name string) types.Type {
	obj := types.NewTypeName(0, types.NewPackage("example.com/app", "app"), name, nil)
	return types.NewPointer(types.NewNamed(obj, types.NewStruct(nil, nil), nil))
}

// TestAllocateEntryFieldAvoidsDerivedMethodCollisions: a scope member's
// field name also decides two method names, so reserving the field alone
// is not enough. A member type named Result would take the field `result`
// and give the entry a stopResult method beside its own stopResult field —
// which the generator wrote and the compiler rejected.
func TestAllocateEntryFieldAvoidsDerivedMethodCollisions(t *testing.T) {
	a := NewNameAllocator()
	for _, reserved := range entryReservedFields {
		a.AllocateName(reserved)
	}

	got := allocateEntryField(a, baseName(namedType("Result")))
	if got == "result" {
		t.Fatal("Result took the field `result`, whose derived stopResult collides with the entry's own stopResult field")
	}
	for _, derived := range []string{"drain" + capitalize(got), "stop" + capitalize(got)} {
		if a.Free(derived) {
			t.Errorf("%s was left unclaimed, so a later member could take it", derived)
		}
	}
}

// TestAllocateEntryFieldAvoidsMemberToMemberCollisions: two members named
// Foo and StopFoo derive the same stopFoo method from different fields.
func TestAllocateEntryFieldAvoidsMemberToMemberCollisions(t *testing.T) {
	a := NewNameAllocator()
	foo := allocateEntryField(a, baseName(namedType("Foo")))
	stopFoo := allocateEntryField(a, baseName(namedType("StopFoo")))

	if "stop"+capitalize(foo) == "stop"+capitalize(stopFoo) {
		t.Fatalf("Foo -> %q and StopFoo -> %q derive the same stop method", foo, stopFoo)
	}
	if stopFoo == "stop"+capitalize(foo) {
		t.Fatalf("StopFoo took the field %q, which is Foo's derived stop method name", stopFoo)
	}
}

// TestNameAllocatorNeverHandsOutANameTwice: the allocator used to count
// bases rather than the names it emitted, so a base spelled like an
// earlier base's suffixed name was handed that same name — pool, pool,
// pool2 gave "pool2" twice, and the generated file declared it twice.
func TestNameAllocatorNeverHandsOutANameTwice(t *testing.T) {
	a := NewNameAllocator()
	got := []string{a.AllocateName("pool"), a.AllocateName("pool"), a.AllocateName("pool2"), a.AllocateName("pool")}
	want := []string{"pool", "pool2", "pool22", "pool3"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allocation %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
	if a.Free("pool2") {
		t.Error("Free(pool2) = true after pool2 was handed out")
	}
}

// TestNameAllocatorSkipsASuffixThatSpellsAPredeclaredName: the suffix is
// appended to the base, so a base ending in a digit can land on a builtin
// — "int3" asked for twice would otherwise shadow int32.
func TestNameAllocatorSkipsASuffixThatSpellsAPredeclaredName(t *testing.T) {
	a := NewNameAllocator()
	if got := a.AllocateName("int3"); got != "int3" {
		t.Fatalf("first int3 = %q, want int3", got)
	}
	if got := a.AllocateName("int3"); got != "int33" {
		t.Errorf("second int3 = %q, want int33: int32 is the builtin type", got)
	}
}

// TestNameAllocatorReserveHoldsTheExactName: a reserved name is taken as
// written, so the first allocation of it moves on to a suffix.
func TestNameAllocatorReserveHoldsTheExactName(t *testing.T) {
	a := NewNameAllocator()
	a.Reserve("httpServer")
	if a.Free("httpServer") {
		t.Fatal("a reserved name reports itself free")
	}
	if got := a.AllocateName("httpServer"); got != "httpServer2" {
		t.Errorf("AllocateName(httpServer) = %q, want httpServer2", got)
	}
}

// FuzzNameAllocatorHandsOutDistinctIdentifiers: whatever order the bases
// arrive in and however they are spelled, every name handed out is a
// legal identifier, neither a keyword nor a predeclared name, and never
// one handed out before.
func FuzzNameAllocatorHandsOutDistinctIdentifiers(f *testing.F) {
	for _, seed := range []string{"pool,pool,pool2,pool", "len,len2,len", "int3,int3,int32", "range,range2,range,range3", "a,a2,a,a22,a2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, list string) {
		a := NewNameAllocator()
		from := map[string]string{}
		for _, base := range strings.Split(list, ",") {
			if !token.IsIdentifier(base) && !token.IsKeyword(base) {
				continue
			}
			got := a.AllocateName(base)
			if prev, dup := from[got]; dup {
				t.Fatalf("%q handed out twice: for base %q and for base %q", got, prev, base)
			}
			if !token.IsIdentifier(got) || shouldAvoidBare(got) {
				t.Fatalf("base %q got %q, which generated code cannot declare cleanly", base, got)
			}
			from[got] = base
		}
	})
}
