//go:build servoinject && prod

package main

import (
	"example.com/servovariants/api"
	"example.com/servovariants/postgres"
	"example.com/servovariants/store"
	"github.com/okian/servo/v3/servo"
)

func wireProd() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *postgres.DB](),
	)
}
