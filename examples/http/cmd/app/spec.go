//go:build servoinject

// The spec: servo.HTTP(...) opts this injector into serving the module's
// //servo: route directives — the default group plus the two declared
// ones, each on its own listener — and attaches middleware, shipped and
// hand-written alike: Recover and RequestID (from servo's middleware
// package, zero config) wrap every group, CORS (configured by
// api.NewCORSConfig) wraps only the public default group, and the module's
// own mw.Auth guards the internal one. Extract makes every handler
// parameter of type *mw.User a per-request value. No servo.Root is needed —
// the handlers' own dependencies pull everything the app requires into the
// graph.
package main

import (
	"example.com/servohttp/mw"
	"github.com/okian/servo/v3/middleware"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Group("internal"),
			servo.Use[*middleware.Recover](),
			servo.Use[*middleware.RequestID](),
			servo.Use[*middleware.CORS](servo.Group("default")),
			servo.Use[*mw.Auth](servo.Group("internal")),
		),
		servo.Extract[*mw.UserExtractor](),
	)
}
