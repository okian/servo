//go:build servoinject

package main

import (
	"example.com/servobasic/migrator"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*migrator.Migrator](),
	)
}
