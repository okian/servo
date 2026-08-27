# 2. Project setup

## Initializing the module

```
mkdir servoorders && cd servoorders
go mod init example.com/servoorders
go get github.com/okian/servo/v3
```

(The tutorial's own copy lives at [`examples/tutorial`](../../examples/tutorial) inside the servo
repo, and points `github.com/okian/servo/v3` at the local checkout with a `replace` directive
instead of a published version — that's specific to developing servo itself. In your own project,
plain `go get github.com/okian/servo/v3` is all you need.)

## Layout

This is where the finished project ends up. You won't have all of this after this chapter — it's
here so you know where each later chapter's code will land.

```
servoorders/
  go.mod
  Makefile                 local dev commands (see below)
  config/                  typed configuration (chapter 3)
  domain/                  core types, no dependencies on anything else here (chapter 4)
  repository/              OrderRepository, UserRepository interfaces (chapter 5)
  postgres/                Postgres implementation of both (chapter 5)
  migrations/              embedded SQL, applied on startup (chapter 5)
  cache/                   OrderCache interface (chapter 6)
  redis/                   Redis implementation (chapter 6)
  broker/                  EventPublisher interface (chapter 7)
  nats/                    NATS implementation (chapter 7)
  notifier/                a subscriber consuming events (chapter 7)
  service/                 OrderService: the business logic (chapter 8)
  auth/                    JWT issuing and verification (chapter 9)
  api/                     HTTP server, router, handlers (chapter 10)
  cmd/orders/              spec.go + main.go — the injector (chapter 11)
  observability/           logging, metrics, tracing setup (chapter 12)
  resilience/              circuit breaker, rate limiting (chapter 13)
  mocks/                   generated mocks for tests (chapter 14)
  deploy/                  docker-compose.yml, Dockerfile (chapter 16)
  openapi.yaml             API contract (chapter 10)
```

Every package is flat — no `internal/` nesting. That's a deliberate choice, not an oversight: this
module has one consumer (you, running it), so there's nothing `internal/` would protect against
that a normal package boundary doesn't already provide. A library with external consumers is a
different situation; see [`internal/` in servo's own layout](../../ARCHITECTURE.md) for a case
where it does matter.

## Tools you'll want installed

| Tool | Used for | Install |
|---|---|---|
| `servo` | Generating and checking the wiring (chapter 11) | `go install github.com/okian/servo/v3/cmd/servo@latest` |
| Docker + `docker compose` | Running Postgres, Redis, NATS locally | [docs.docker.com](https://docs.docker.com/get-docker/) |
| `golangci-lint` | CI lint step (chapter 15) | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |

You don't need `servo` installed globally to follow along — every command in this tutorial also
works as `go run github.com/okian/servo/v3/cmd/servo <command>`, which is what the Makefile below
actually uses, so a fresh clone works with nothing pre-installed except Go and Docker.

## The Makefile

Real projects accumulate a handful of commands everyone on the team needs to remember (start the
local dependencies, run migrations, run the fast tests, run the slow ones). A `Makefile` is a
low-ceremony place to put them:

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

`test` and `test-integration` are deliberately separate targets. `test` never touches the network —
every package skips its integration tests when the corresponding `TEST_*_DSN`/`TEST_*_ADDR`
variable is unset, so it's always safe to run and always fast. `test-integration` is what actually
exercises Postgres, Redis, and NATS for real; it needs `make up` first. [Chapter
14](14-testing-strategy.md) explains why the split is worth keeping once the suite grows.

## Diagnostics

- **`go: github.com/okian/servo/v3@...: reading github.com/okian/...: 404 Not Found`** — you're
  following along outside the servo repo and haven't run `go get github.com/okian/servo/v3` yet
  (or it hasn't been published as a real module yet, if you're doing this against an unpublished
  fork — use a `replace` directive pointing at your local checkout in that case, exactly as
  `examples/tutorial/go.mod` does for servo's own repo).
- **`make: docker: command not found`** — install Docker first; nothing past chapter 5 works
  without it.

## Do's and don'ts

- **Do** commit `go.sum` — it's what makes `go build` reproducible across machines. Never add it to
  `.gitignore`.
- **Don't** reach for a task runner heavier than `make` until `make` actually runs out of
  expressiveness (usually: needing real conditionals, or cross-platform `sh` differences). Five
  targets doesn't justify [`just`](https://github.com/casey/just) or a shell-script framework yet.

## Next

[Chapter 3: Configuration](03-configuration.md) — the first package this service actually needs.
