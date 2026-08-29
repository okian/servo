# Building a microservice with servo

A step-by-step, from-scratch build of a real order-management service — HTTP API, JWT auth,
Postgres, Redis, NATS, Prometheus metrics, OpenTelemetry tracing, a circuit breaker, a full test
suite, CI/CD, and a Docker deployment — using [servo](https://github.com/okian/servo/blob/master/README.md) to wire and run all of it.

Every chapter's code lives in [`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial), a real, separate Go
module you can `cd` into and run at every step. Nothing in these pages is invented: every code
block is copied from that module, and every command's output shown here was actually run.

## Why this exists

servo's own [README](https://github.com/okian/servo/blob/master/README.md) and [`examples/basic`](https://github.com/okian/servo/tree/master/examples/basic) show the
mechanics — how one constructor gets wired to another, what a spec file looks like, what
`servo generate` produces. What they don't show is what it's like to build something with real
weight to it: a service with a database, a cache, an event to publish, a login flow, tests at
three different levels, and a pipeline that ships it. That gap — "I understand what servo does" to
"I could build a real service with it" — is what this tutorial is for. Every chapter adds one
layer or concern to the same running service, in the order you'd actually add them, so by the last
chapter nothing is hypothetical: it's a service you've built, one working piece at a time, that
happens to also be a complete reference for how the pieces fit together.

## How to read this

Each chapter follows the same shape: what problem this layer solves and why it exists, the real
code that solves it (with enough surrounding explanation that you're not just staring at syntax),
a diagram where the shape of something is easier to see than to describe, a "try it yourself"
section with commands you can actually run, then diagnostics, do's and don'ts, and pointers to
alternatives. Read chapters in order the first time — each one builds on code the previous ones
already wrote. After that, they hold up fine as standalone reference.

## Who this is for

You should be comfortable writing Go and using `go test`, but this tutorial assumes nothing about
prior exposure to dependency injection, microservice architecture, or any of the specific
libraries used (Postgres, Redis, NATS, JWT, Prometheus, OpenTelemetry). Each is introduced when
it's first needed, with a short "what and why" before the code.

## Prerequisites

- Go 1.27 or newer (`go version`)
- [Docker](https://docs.docker.com/get-docker/) with the `docker compose` plugin, for running
  Postgres, Redis, and NATS locally
- `curl` (or any HTTP client) for the "try it yourself" sections
- No cloud account of any kind is required — everything runs on your machine

## What you'll build

```mermaid
flowchart TB
    Client(["HTTP client"])

    subgraph api["API layer"]
        Router["Router + middleware<br/>(auth, logging, recovery, rate limit)"]
    end

    subgraph svc["Service layer"]
        OrderService
    end

    subgraph data["Data"]
        PG[("Postgres")]
        Redis[("Redis")]
    end

    subgraph msg["Messaging"]
        NATS[("NATS")]
        Notifier["notifier<br/>(subscriber)"]
    end

    Client -->|"Bearer JWT"| Router
    Router --> OrderService
    OrderService -->|"read/write orders"| PG
    OrderService -->|"cache-aside reads"| Redis
    OrderService -->|"publish OrderPlaced"| NATS
    NATS -->|"consume"| Notifier
```

A user logs in, gets a JWT, and can place and view orders. Placing an order writes to Postgres,
invalidates the cache, and publishes an `OrderPlaced` event; a separate `notifier` component
consumes that event to show the "other side" of an event-driven system without needing an actual
second service. Every arrow in that diagram is a real dependency `servo` resolves and wires for
you — nothing here is assembled by hand.

## Chapters

| # | Chapter | What it covers |
|---|---------|-----------------|
| 1 | [Architecture overview](01-architecture-overview.md) | Layers, why layered, where servo fits |
| 2 | [Project setup](02-project-setup.md) | Module layout, tools, the Makefile |
| 3 | [Configuration](03-configuration.md) | Typed env config, validation, secrets |
| 4 | [Domain layer](04-domain-layer.md) | Core types, framework-free |
| 5 | [Repository layer](05-repository-layer.md) | Postgres, migrations, connection pooling |
| 6 | [Caching layer](06-caching-layer.md) | Redis, cache-aside, invalidation |
| 7 | [Messaging layer](07-messaging-layer.md) | NATS, publish/subscribe, delivery guarantees |
| 8 | [Service layer](08-service-layer.md) | Business logic, orchestration, domain errors |
| 9 | [Authentication](09-authentication.md) | JWT issue/verify, password hashing, middleware |
| 10 | [API layer](10-api-layer.md) | Routing, DTOs, validation, error mapping |
| 11 | [Wiring with servo](11-wiring-with-servo.md) | The spec file, capabilities, `servo generate` |
| 12 | [Scoped instances](12-scoped-instances.md) | One session per user, instead of one per process |
| 13 | [Observability](13-observability.md) | Structured logs, metrics, tracing, health checks |
| 14 | [Resilience](14-resilience.md) | Circuit breaker, rate limiting, graceful shutdown |
| 15 | [Testing strategy](15-testing-strategy.md) | Unit, integration, and API-level tests |
| 16 | [CI/CD](16-cicd.md) | GitHub Actions: lint, test, build, `servo check` |
| 17 | [Running and deployment](17-running-and-deployment.md) | Docker Compose, Dockerfile, env reference |
| 18 | [Troubleshooting](18-troubleshooting.md) | Every diagnostic from every chapter, organized by symptom |
| 19 | [Alternatives and further reading](19-alternatives-and-further-reading.md) | Other valid choices at every layer |

Read them in order the first time through — each one assumes the code from the previous chapters
already exists. After that, they stand alone well enough to use as reference.
