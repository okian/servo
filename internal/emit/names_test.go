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
