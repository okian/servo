//go:build servoinject

package main

import (
	"time"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/ginapi"
	"example.com/servoorders/internal/mocks"
	"example.com/servoorders/internal/natsbroker"
	"example.com/servoorders/internal/notifier"
	"example.com/servoorders/internal/postgres"
	"example.com/servoorders/internal/repository"
	"example.com/servoorders/internal/resilience"
	"example.com/servoorders/internal/session"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*ginapi.Server](),
		servo.Root[*notifier.Notifier](),

		// One session per user, not one per process. Linger keeps it
		// alive across the gap between two requests from the same person;
		// Max is the cap that stops an unauthenticated flood from
		// allocating without bound. See
		// docs/tutorial/14-scoped-instances.md.
		servo.Scoped[*session.Session, session.Sessions](
			servo.Linger(5*time.Minute),
			servo.Max(50_000),
		),

		servo.Bind[repository.OrderRepository, *postgres.Store](),
		servo.Bind[repository.UserRepository, *postgres.Store](),
		servo.Bind[broker.EventPublisher, *natsbroker.Publisher](),

		// The service layer gets the circuit-breaker-wrapped cache, not
		// redis.Cache directly — see docs/tutorial/16-resilience.md for
		// why CircuitBreakerCache depends on *redis.Cache concretely
		// rather than on cache.OrderCache itself.
		servo.Bind[cache.OrderCache, *resilience.CircuitBreakerCache](),

		// NewTestApp substitutes all four real infrastructure dependencies
		// with mocks — see docs/tutorial/13-wiring-with-servo.md.
		servo.Override[repository.OrderRepository, *mocks.OrderRepositoryForServo](),
		servo.Override[repository.UserRepository, *mocks.UserRepositoryForServo](),
		servo.Override[cache.OrderCache, *mocks.OrderCacheForServo](),
		servo.Override[broker.EventPublisher, *mocks.EventPublisherForServo](),
	)
}
