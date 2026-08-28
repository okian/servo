//go:build servoinject

package main

import (
	"time"

	"example.com/servoscoped/api"
	"example.com/servoscoped/chat"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Scoped[*chat.Room, chat.Rooms](
			servo.Linger(30*time.Second),
			servo.Max(10_000),
		),
	)
}
