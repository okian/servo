//go:build servoinject

// The spec: servo.HTTP() opts this injector into serving the module's
// //servo: route directives. No servo.Root is needed — the handlers' own
// dependencies (the store, the *servo.HTTPConfig) pull everything the app
// requires into the graph.
package main

import (
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.HTTP(),
	)
}
