//go:build servoinject

// Package wiring holds the marker set all three injectors share.
//
// cmd/orders, cmd/ordersgin and cmd/ordersgrpc wire the same service layer
// behind three transports, so everything below the transport is identical
// in all three — ten of each spec's eleven marker calls, copy-pasted three
// times, where adding a fifth Bind or changing Max was a three-file edit
// nothing checked. servo.Include splices this list into each spec, which
// then carries only what actually differs: its own transport's Root.
//
// The build tag is required, and for the same reason a spec file needs
// one: every marker below panics if it is ever executed, so this file must
// not compile into the real binary. `servo generate` refuses an included
// set without it, and servo-vet flags the calls in your editor.
package wiring

import (
	"time"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/broker/natsbroker"
	"example.com/servoorders/internal/broker/notifier"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/mocks"
	"example.com/servoorders/internal/repository"
	"example.com/servoorders/internal/repository/postgres"
	"example.com/servoorders/internal/resilience"
	"example.com/servoorders/internal/session"
	"github.com/okian/servo/v3/servo"
)

// Shared is everything below the transport: the notifier root, the session
// scope, the four bindings, and the four test overrides.
func Shared() []servo.Marker {
	return []servo.Marker{
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
	}
}
