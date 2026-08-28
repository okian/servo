package servo

import (
	"errors"
	"testing"
	"time"
)

func TestLingerWindowOverride(t *testing.T) {
	prev := LingerOverride
	t.Cleanup(func() { LingerOverride = prev })

	// -1, the default, means "no override": the declared window stands.
	LingerOverride = -1
	if got := LingerWindow(30 * time.Second); got != 30*time.Second {
		t.Fatalf("LingerWindow = %s, want the declared window", got)
	}
	// Zero is a real override — die with the last holder — not an absent
	// one, which is why the sentinel is negative rather than zero.
	LingerOverride = 0
	if got := LingerWindow(30 * time.Second); got != 0 {
		t.Fatalf("LingerWindow = %s, want 0", got)
	}
	LingerOverride = 5 * time.Millisecond
	if got := LingerWindow(30 * time.Second); got != 5*time.Millisecond {
		t.Fatalf("LingerWindow = %s, want the override", got)
	}
}

func TestScopeMarkersPanic(t *testing.T) {
	for name, fn := range map[string]func(){
		"Scoped": func() { Scoped[int, error]() },
		"Linger": func() { Linger(time.Second) },
		"Max":    func() { Max(10) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("servo.%s did not panic — a marker that ran means the build tag went missing", name)
				}
			}()
			fn()
		})
	}
}

// TestScopeErrorsAreDistinct guards against a copy-paste that would make
// two failure modes indistinguishable to errors.Is.
func TestScopeErrorsAreDistinct(t *testing.T) {
	all := []error{ErrNoScopeKey, ErrNoLifetime, ErrScopeFull, ErrScopeClosed}
	for i, a := range all {
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Fatalf("%v and %v are the same error", a, b)
			}
		}
	}
}
