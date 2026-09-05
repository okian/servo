//go:build servoinject

// The spec: servo.HTTP(...) opts this injector into serving the module's
// //servo: route directives — the default group plus the two declared
// ones, each on its own listener — and attaches middleware: RequestID
// wraps every group, Auth only the internal one. Extract makes every
// handler parameter of type *mw.User a per-request value. No servo.Root is
// needed — the handlers' own dependencies pull everything the app requires
// into the graph.
package main

import (
	"example.com/servohttp/mw"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.HTTP(
			servo.Group("telemetry"),
			servo.Group("internal"),
			servo.Use[*mw.RequestID](),
			servo.Use[*mw.Auth](servo.Group("internal")),
		),
		servo.Extract[*mw.UserExtractor](),
	)
}
