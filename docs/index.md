# servo

Servo is a build-time code generator that resolves a Go application's object graph from
constructor signatures and emits plain Go source that constructs, starts, supervises, and shuts
down the application in dependency order.

**No reflection. No runtime registry. No `init()`. No hand-written wiring.**

The generated file is ordinary Go: compiler-checked, IDE-navigable, steppable in a debugger, and
readable by a human at 3am.

```
go install github.com/okian/servo/v3/cmd/servo@latest
```

- [Repository](https://github.com/okian/servo) — README, quick start, and the `servo` CLI
- [Architecture](https://github.com/okian/servo/blob/master/ARCHITECTURE.md) — how the load → scan → resolve → emit pipeline fits together
- [Changelog](https://github.com/okian/servo/blob/master/CHANGELOG.md) — what's changed, and how this project versions releases
- [API reference](https://pkg.go.dev/github.com/okian/servo/v3) on pkg.go.dev

## Building a microservice with servo

A step-by-step, from-scratch build of a real order-management service — HTTP API, JWT auth,
Postgres, Redis, NATS, Prometheus metrics, OpenTelemetry tracing, a circuit breaker, a full test
suite, CI/CD, and a Docker deployment.

Every chapter's code lives in
[`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial), a real,
separate Go module you can `cd` into and run at every step. Start with the
[tutorial introduction](tutorial/) for how to read it and who it's for.

| # | Chapter |
|---|---|
| 1 | [Architecture overview](tutorial/01-architecture-overview.md) |
| 2 | [Project setup](tutorial/02-project-setup.md) |
| 3 | [Configuration](tutorial/03-configuration.md) |
| 4 | [Domain layer](tutorial/04-domain-layer.md) |
| 5 | [Repository layer](tutorial/05-repository-layer.md) |
| 6 | [Caching layer](tutorial/06-caching-layer.md) |
| 7 | [Messaging layer](tutorial/07-messaging-layer.md) |
| 8 | [Service layer](tutorial/08-service-layer.md) |
| 9 | [Authentication](tutorial/09-authentication.md) |
| 10 | [API layer](tutorial/10-api-layer.md) |
| 11 | [Wiring with servo](tutorial/11-wiring-with-servo.md) |
| 12 | [Observability](tutorial/12-observability.md) |
| 13 | [Resilience](tutorial/13-resilience.md) |
| 14 | [Testing strategy](tutorial/14-testing-strategy.md) |
| 15 | [CI/CD](tutorial/15-cicd.md) |
| 16 | [Running and deployment](tutorial/16-running-and-deployment.md) |
| 17 | [Troubleshooting](tutorial/17-troubleshooting.md) |
| 18 | [Alternatives and further reading](tutorial/18-alternatives-and-further-reading.md) |
