//go:build servoinject

package main

import (
	"example.com/servobasic/migrator"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*migrator.Migrator](),

		// The target schema version is a command-line flag, so no
		// constructor can produce it. Declaring it here makes it a node
		// like any other — resolved by type, checked at generation time —
		// and puts it on the generated Values struct, which NewWith takes.
		servo.Value[migrator.Target](),
	)
}
