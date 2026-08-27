# 2. Project setup

Before writing anything, let's set up the module and get oriented — where files will go, what
tools you'll want on hand, and the handful of commands you'll be running constantly for the rest
of this tutorial. None of this is exciting on its own, but skipping it means stopping to figure
this out later, in the middle of a chapter where it'll be more disruptive.

## Create the module

```
mkdir servoorders && cd servoorders
go mod init example.com/servoorders
go get github.com/okian/servo/v3
```

That's the whole setup — one module, one dependency so far. (The tutorial's own copy, at
[`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial) inside the servo repo, points
`github.com/okian/servo/v3` at the local checkout with a `replace` directive instead of a published
version, because it's developing servo itself alongside the tutorial. You won't need that; plain
`go get` is enough.)

## Where things will go

You don't need to create any of this yet — each directory shows up in the chapter that actually
needs it. Keep this page open as a map for "where does this belong":

```
servoorders/
  go.mod
  Makefile                 local dev commands (below)
  config/                  typed configuration                 chapter 3
  domain/                  core types, no dependency on anything else here   chapter 4
  repository/              OrderRepository, UserRepository interfaces        chapter 5
  postgres/                Postgres implementation of both                   chapter 5
  migrations/              embedded SQL, applied on startup                  chapter 5
  cache/                   OrderCache interface                              chapter 6
  redis/                   Redis implementation                              chapter 6
  broker/                  EventPublisher interface                          chapter 7
  natsbroker/              NATS implementation                               chapter 7
  notifier/                a subscriber consuming events                     chapter 7
  service/                 OrderService: the business logic                  chapter 8
  auth/                    JWT issuing and verification                     chapter 9
  api/                     HTTP server, router, handlers                     chapter 10
  cmd/orders/              spec.go + main.go — the injector                  chapter 11
  observability/           logging, metrics, tracing setup                   chapter 12
  resilience/              circuit breaker, rate limiting                    chapter 13
  mocks/                   generated mocks for tests                        chapter 8 onward
  deploy/                  docker-compose.yml, Dockerfile                    chapter 16
  openapi.yaml             API contract                                     chapter 10
```

One thing worth deciding now, before it's a habit: every package here is flat, with no `internal/`
nesting. That's deliberate — this module has exactly one consumer (you, running it), so there's
nothing `internal/` would protect against that an ordinary package boundary doesn't already give
you. A library meant for other people to import is a different situation; servo's own
[`internal/` layout](https://github.com/okian/servo/blob/master/ARCHITECTURE.md) is worth a look for what that case looks like.

## Install what you'll need

| Tool | You'll use it for | Install |
|---|---|---|
| `servo` | Generating and checking the wiring (chapter 11) | `go install github.com/okian/servo/v3/cmd/servo@latest` |
| Docker + `docker compose` | Running Postgres, Redis, NATS locally | [docs.docker.com](https://docs.docker.com/get-docker/) |
| `golangci-lint` | The CI lint step (chapter 15) | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |

You can actually skip installing `servo` globally — every command in this tutorial works equally
well as `go run github.com/okian/servo/v3/cmd/servo <command>`, and that's what the Makefile below
uses, so a fresh clone of the finished project works with nothing pre-installed except Go and
Docker.

## Write the Makefile

Add a `Makefile` at the root now. You'll be reaching for these commands from chapter 5 onward, and
it's one less thing to assemble under pressure later:

```makefile
.PHONY: up down test test-integration run generate check

up:
	docker compose -f deploy/docker-compose.yml up -d

down:
	docker compose -f deploy/docker-compose.yml down -v

test:
	go test ./...

test-integration:
	TEST_POSTGRES_DSN="postgres://orders:orders@localhost:5432/orders?sslmode=disable" \
	TEST_REDIS_ADDR="localhost:6379" \
	TEST_NATS_URL="nats://localhost:4222" \
	go test ./... -v

run:
	go run ./cmd/orders

generate:
	go run github.com/okian/servo/v3/cmd/servo generate

check:
	go run github.com/okian/servo/v3/cmd/servo check
```

Two test targets, not one, and that split is worth understanding now rather than discovering by
accident: `test` never touches the network. Every package we write will skip its own integration
tests automatically when the matching `TEST_*_DSN`/`TEST_*_ADDR` variable is unset, so `make test`
stays safe to run constantly, from anywhere, with nothing running in the background. `make
test-integration` is the one that actually needs Postgres, Redis, and NATS up first (`make up`) —
you'll use it starting in chapter 5, the moment there's a real database to test against.

## Diagnostics

- **`go: github.com/okian/servo/v3@...: reading github.com/okian/...: 404 Not Found`** — you're
  following along outside the servo repo and haven't run `go get github.com/okian/servo/v3` yet
  (or you're pointed at an unpublished fork — use a `replace` directive at your local checkout in
  that case, the same way `examples/tutorial/go.mod` does).
- **`make: docker: command not found`** — install Docker before continuing; nothing from chapter 5
  onward works without it.

## Do's and don'ts

- **Do** commit `go.sum` once it exists — it's what makes `go build` reproducible across machines.
  Never gitignore it.
- **Don't** reach for a task runner heavier than `make` until `make` genuinely runs out of
  expressiveness (usually: real conditionals, or cross-platform `sh` differences). Five targets
  doesn't justify [`just`](https://github.com/casey/just) or a shell-script framework yet.

## Next

[Chapter 3: Configuration](03-configuration.md) — the first package this service actually needs,
and the first thing every other package will depend on.
