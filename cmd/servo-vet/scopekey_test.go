package main

import (
	"strings"
	"testing"
)

// The blank-receiver rule is the one servo cannot delegate to the
// compiler: generated code calls ScopeKey on a typed nil, and no
// signature can say "never dereferences the receiver".

const blankReceiverSrc = `//go:build servoinject

package fixture

import "context"

type RoomKey string

type Room struct{}

func (_ *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
`

const anonReceiverSrc = `//go:build servoinject

package fixture

import "context"

type RoomKey string

type Room struct{}

func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
`

const namedReceiverSrc = `//go:build servoinject

package fixture

import "context"

type RoomKey string

type Room struct{ id RoomKey }

func (r *Room) ScopeKey(ctx context.Context) (RoomKey, error) { return r.id, nil }
`

// Unrelated is the false-positive guard: a method that merely shares the
// name but has none of the extractor's shape is some other package's
// business.
const unrelatedScopeKeySrc = `//go:build servoinject

package fixture

import "context"

type Cache struct{ prefix string }

func (c *Cache) ScopeKey(ctx context.Context) (string, error) { return c.prefix, nil }

func (c *Cache) ScopeKeyFor(k int) string { return c.prefix }

type Bare struct{ n int }

func (b *Bare) ScopeKey() (int, error) { return b.n, nil }

func ScopeKey(ctx context.Context) (int, error) { return 0, nil }
`

func TestFlagsNamedScopeKeyReceiver(t *testing.T) {
	got := runOn(t, namedReceiverSrc)
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(got), got)
	}
	if !strings.Contains(got[0], "must not name its receiver") {
		t.Fatalf("diagnostic = %q, want it to name the unnamed-receiver rule", got[0])
	}
}

func TestAcceptsUnreachableScopeKeyReceivers(t *testing.T) {
	for name, src := range map[string]string{
		"blank": blankReceiverSrc,
		"anon":  anonReceiverSrc,
	} {
		t.Run(name, func(t *testing.T) {
			if got := runOn(t, src); len(got) != 0 {
				t.Fatalf("got %v, want no diagnostics", got)
			}
		})
	}
}

// TestIgnoresMethodsThatOnlyShareTheName covers every way a ScopeKey-named
// declaration can fail the extractor shape: an undefined key type, a
// missing context, and a plain function with no receiver at all.
func TestIgnoresMethodsThatOnlyShareTheName(t *testing.T) {
	if got := runOn(t, unrelatedScopeKeySrc); len(got) != 0 {
		t.Fatalf("got %v, want no diagnostics — none of these is a key extractor", got)
	}
}

// TestFlagsScopeKeyInUntaggedFile is the interaction between the two
// rules: the receiver check runs everywhere, not only in spec files.
func TestFlagsScopeKeyInUntaggedFile(t *testing.T) {
	src := strings.Replace(namedReceiverSrc, "//go:build servoinject\n\n", "", 1)
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "must not name its receiver") {
		t.Fatalf("got %v, want the unnamed-receiver diagnostic in an ordinary file too", got)
	}
}

// The shape gate has to be narrow enough that an unrelated method named
// ScopeKey is none of this analyzer's business, and wide enough that a
// real extractor with a named receiver is. These cover the branches the
// two fixtures above don't reach.

const shapeEdgeCasesSrc = `package fixture

import "context"

type RoomKey string

// A named receiver on each, so anything the gate accepts gets reported —
// which makes "no diagnostic" mean "the gate rejected the shape".

// WrongSecondResult's second result is not error.
type WrongSecondResult struct{ n int }

func (w *WrongSecondResult) ScopeKey(ctx context.Context) (RoomKey, bool) { return "", false }

// TooManyResults returns three.
type TooManyResults struct{ n int }

func (t *TooManyResults) ScopeKey(ctx context.Context) (RoomKey, RoomKey, error) { return "", "", nil }

// IfaceKeyType's key is an interface, whose dynamic types would never
// compare equal across callers.
type Named interface{ Name() string }

type IfaceKeyType struct{ n int }

func (i *IfaceKeyType) ScopeKey(ctx context.Context) (Named, error) { return nil, nil }

// UndefinedKeyType returns a bare string.
type UndefinedKeyType struct{ n int }

func (u *UndefinedKeyType) ScopeKey(ctx context.Context) (string, error) { return "", nil }
`

func TestIgnoresShapesThatAreNotExtractors(t *testing.T) {
	if got := runOn(t, shapeEdgeCasesSrc); len(got) != 0 {
		t.Fatalf("got %v, want no diagnostics — none of these is a key extractor", got)
	}
}

// TestFlagsARealExtractorWithADependency is the mirror: extra parameters
// after ctx are ordinary dependencies, so the shape is still an
// extractor's and a named receiver still has to be reported.
func TestFlagsARealExtractorWithADependency(t *testing.T) {
	const src = `package fixture

import "context"

type RoomKey string
type Cfg struct{}

type Room struct{ id RoomKey }

func (r *Room) ScopeKey(ctx context.Context, c *Cfg) (RoomKey, error) { return r.id, nil }
`
	got := runOn(t, src)
	if len(got) != 1 || !strings.Contains(got[0], "must not name its receiver") {
		t.Fatalf("got %v, want the unnamed-receiver diagnostic", got)
	}
}
