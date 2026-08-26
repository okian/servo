//go:build servoinject

package main

import (
	"example.com/servobasic/api"
	"example.com/servobasic/mockstore"
	"example.com/servobasic/postgres"
	"example.com/servobasic/relay"
	"example.com/servobasic/store"
	"example.com/servobasic/worker"
	"github.com/okian/servo/v2/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*worker.Consumer](),
		servo.Root[*relay.Relay](),
		servo.Bind[store.Store, *postgres.DB](),
		servo.Override[store.Store, *mockstore.Store](),
	)
}
