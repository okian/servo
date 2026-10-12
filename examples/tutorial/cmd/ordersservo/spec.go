//go:build servoinject

package main

import (
	"time"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/broker/natsbroker"
	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/mocks"
	"example.com/servoorders/internal/observability"
	"example.com/servoorders/internal/repository"
	"example.com/servoorders/internal/repository/postgres"
	"example.com/servoorders/internal/resilience"
	"example.com/servoorders/internal/session"
	"example.com/servoorders/internal/transport/servoapi"
	"github.com/okian/servo/v3/middleware"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		// No Root for the transport: servo.HTTP() emits the server, and
		// the handlers' own parameters pull the services into the graph.
		// The middleware chain is the api transport's, in the same
		// outermost-first order its server.go builds by hand — Tracer,
		// Metrics and RateLimiter already had the wrap method, so they
		// attach unchanged; recover and logging come from servo's shipped
		// middleware package.
		servo.HTTP(
			servo.Use[*middleware.Recover](),
			servo.Use[*resilience.RateLimiter](),
			servo.Use[*observability.Tracer](),
			servo.Use[*observability.Metrics](),
			servo.Use[*middleware.AccessLog](),
			servo.Use[*servoapi.Auth](
				servo.Route("POST /orders"),
				servo.Route("GET /orders/{id}"),
				servo.Route("GET /orders"),
				servo.Route("GET /me/recent"),
			),
		),
		servo.Extract[*servoapi.ClaimsExtractor](),

		servo.Scoped[*session.Session, session.Sessions](
			servo.Linger(5*time.Minute),
			servo.Max(50_000),
		),

		servo.Bind[repository.OrderRepository, *postgres.Store](),
		servo.Bind[repository.UserRepository, *postgres.Store](),
		servo.Bind[broker.EventPublisher, *natsbroker.Publisher](),
		servo.Bind[cache.OrderCache, *resilience.CircuitBreakerCache](),

		servo.Override[repository.OrderRepository, *mocks.OrderRepositoryForServo](),
		servo.Override[repository.UserRepository, *mocks.UserRepositoryForServo](),
		servo.Override[cache.OrderCache, *mocks.OrderCacheForServo](),
		servo.Override[broker.EventPublisher, *mocks.EventPublisherForServo](),

		// Every consumer of these four interfaces gets a generated
		// OpenTelemetry wrapper: one span per method call, a child of the
		// request span Tracer.Middleware opened, with nothing instrumented
		// by hand. The wrapper takes *observability.Tracer as its
		// trace.TracerProvider. A not-found and a cache miss are results,
		// not failures, so they stay off the span. See
		// docs/tutorial/16-observability.md.
		servo.Trace[repository.OrderRepository](servo.Expected(domain.ErrNotFound)),
		servo.Trace[repository.UserRepository](servo.Expected(domain.ErrNotFound)),
		servo.Trace[cache.OrderCache](servo.Expected(cache.ErrMiss)),
		servo.Trace[broker.EventPublisher](),
	)
}
