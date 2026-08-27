//go:build servoinject

package main

import (
	"example.com/servomocking/api"
	"example.com/servomocking/mockery/mocks"
	"example.com/servomocking/realstore"
	"example.com/servomocking/store"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *realstore.Store](),
		servo.Override[store.Store, *mocks.StoreMock](),
	)
}
