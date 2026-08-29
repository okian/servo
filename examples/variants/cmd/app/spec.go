//go:build servoinject && !prod

package main

//go:generate go run github.com/okian/servo/v3/cmd/servo generate

import (
	"example.com/servovariants/api"
	"example.com/servovariants/memory"
	"example.com/servovariants/store"
	"github.com/okian/servo/v3/servo"
)

// The `&& !prod` is the whole trick. Servo mirrors this constraint into the
// generated file, so servo_gen.go is gated `!servoinject && !prod` and
// cannot compile alongside the prod variant. Servo never invents that
// negation — write it here, or servo refuses to generate the second
// variant.
func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
	)
}
