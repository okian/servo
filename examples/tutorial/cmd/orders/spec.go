//go:build servoinject

package main

import (
	"example.com/servoorders/api"
	"example.com/servoorders/broker"
	"example.com/servoorders/cache"
	"example.com/servoorders/mocks"
	"example.com/servoorders/natsbroker"
	"example.com/servoorders/notifier"
	"example.com/servoorders/postgres"
	"example.com/servoorders/redis"
	"example.com/servoorders/repository"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*notifier.Notifier](),

		servo.Bind[repository.OrderRepository, *postgres.Store](),
		servo.Bind[repository.UserRepository, *postgres.Store](),
		servo.Bind[cache.OrderCache, *redis.Cache](),
		servo.Bind[broker.EventPublisher, *natsbroker.Publisher](),

		// NewTestApp substitutes all four real infrastructure dependencies
		// with mocks — see docs/tutorial/11-wiring-with-servo.md.
		servo.Override[repository.OrderRepository, *mocks.OrderRepositoryForServo](),
		servo.Override[repository.UserRepository, *mocks.UserRepositoryForServo](),
		servo.Override[cache.OrderCache, *mocks.OrderCacheForServo](),
		servo.Override[broker.EventPublisher, *mocks.EventPublisherForServo](),
	)
}
