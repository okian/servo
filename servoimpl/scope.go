package servoimpl

import (
	"time"

	"github.com/okian/servo/v3/servo"
)

// LingerWindow returns declared unless servo.LingerOverride is active.
// Generated code calls it once per scope, in New, so an override set by
// servotest.Linger has to be in place before New runs.
func LingerWindow(declared time.Duration) time.Duration {
	if servo.LingerOverride >= 0 {
		return servo.LingerOverride
	}
	return declared
}
