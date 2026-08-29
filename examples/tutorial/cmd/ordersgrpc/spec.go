//go:build servoinject

package main

import (
	"example.com/servoorders/internal/transport/grpcapi"
	"example.com/servoorders/internal/wiring"
	"github.com/okian/servo/v3/servo"
)

// Everything below the transport is identical in all three injectors, so it
// lives in one place — see internal/wiring. What is left here is what
// actually differs: this binary's own transport.
func wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*grpcapi.Server](),
	)
}
