package emit

import (
	"go/types"
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

	got := allocateEntryField(a, namedType("Result"))
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
	foo := allocateEntryField(a, namedType("Foo"))
	stopFoo := allocateEntryField(a, namedType("StopFoo"))

	if "stop"+capitalize(foo) == "stop"+capitalize(stopFoo) {
		t.Fatalf("Foo -> %q and StopFoo -> %q derive the same stop method", foo, stopFoo)
	}
	if stopFoo == "stop"+capitalize(foo) {
		t.Fatalf("StopFoo took the field %q, which is Foo's derived stop method name", stopFoo)
	}
}
