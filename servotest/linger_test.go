package servotest

import (
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
)

func TestLingerOverridesAndRestores(t *testing.T) {
	before := servo.LingerOverride

	t.Run("inside", func(t *testing.T) {
		Linger(t, 3*time.Millisecond)
		if servo.LingerOverride != 3*time.Millisecond {
			t.Fatalf("LingerOverride = %s, want the override", servo.LingerOverride)
		}
		if got := servo.LingerWindow(time.Hour); got != 3*time.Millisecond {
			t.Fatalf("LingerWindow = %s, want the override to win", got)
		}
	})

	if servo.LingerOverride != before {
		t.Fatalf("LingerOverride = %s after the subtest, want it restored to %s", servo.LingerOverride, before)
	}
}
