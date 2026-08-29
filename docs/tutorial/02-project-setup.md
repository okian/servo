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
  cmd/orders/              spec.go + main.go — the injector                  chapter 13
  cmd/ordersgin/           the Gin injector                                  chapter 11
  cmd/ordersgrpc/          the gRPC injector                                 chapter 12
  deploy/                  docker-compose.yml, Dockerfile                    chapter 19
  internal/
    config/                typed configuration                               chapter 3
    domain/                core types, no dependency on anything else here   chapter 4

    repository/            OrderRepository, UserRepository interfaces        chapter 5
      postgres/            the implementation of both                        chapter 5
      migrations/          embedded SQL, applied on startup                  chapter 5
    cache/                 OrderCache interface                              chapter 6
      redis/               the implementation                                chapter 6
    broker/                EventPublisher interface                          chapter 7
      natsbroker/          the implementation                                chapter 7
      notifier/            a subscriber consuming events                     chapter 7

    service/               OrderService: the business logic                  chapter 8
    auth/                  JWT issuing and verification                      chapter 9
    session/               per-user state, one instance per logged-in user   chapter 14

    transport/
      api/                 HTTP server, router, handlers                     chapter 10
      ginapi/              the same API in Gin                               chapter 11
      grpcapi/             gRPC and REST sharing one port                    chapter 12
      openapi/             API contract, embedded and served                 chapter 10
      admin/               health/readiness/metrics, on their own port       chapter 15

    observability/         logging, metrics, tracing setup                   chapter 15
    resilience/            circuit breaker, rate limiting                    chapter 16
    mocks/                 generated mocks for tests                         chapter 8 onward
```

Two things about that shape are deliberate.

**An implementation sits under the interface it satisfies.** `repository/` declares
`OrderRepository`; `repository/postgres/` is the only thing that implements it. Same for
`cache/redis` and `broker/natsbroker`. The alternative — `postgres/` and `repository/` as siblings —
reads as two unrelated packages, and you learn which implements which by opening them. Nesting says
it in the path, and it makes the direction of dependency obvious: an adapter imports its port, never
the other way round.

The names still carry their suffixes: `broker/natsbroker` rather than `broker/nats`, because the
NATS client library's own package is called `nats` and a file importing both would need an alias on
one of them. Directory nesting buys the grouping without that collision.

**Everything lives under `internal/`, with `cmd/` holding the entry points.** That is the ordinary
shape for a Go application, and it is worth knowing what it buys, because the mechanism is often
confused with an unrelated one.

Unexported identifiers hide *symbols* within a package. `internal/` restricts who may *import the
package at all*, and it is the only thing in Go that does: a package under `internal/` is importable
only by code rooted at the directory containing that `internal/`. A flat layout leaves every package
publicly importable the moment the module is fetchable — by another repository, or more likely by a
sibling module in the same monorepo — and "nobody will import it" is a policy nothing enforces.

So `internal/` is the default worth reaching for in an application, and the top level is where you
put the handful of packages you genuinely intend other people to use. servo is indifferent either
way: it resolves providers under `internal/` exactly as it does anywhere else, and a spec file in
`cmd/` may import its own module's `internal/` packages, since the restriction is scoped to the
directory containing `internal/`.

The cost is one path segment in every import — `example.com/servoorders/internal/repository/postgres` rather
than `example.com/servoorders/postgres` — which is why plenty of small services skip it. That is a
defensible trade to make deliberately, and a bad one to make by not thinking about it.

servo's own [`internal/` layout](https://github.com/okian/servo/blob/master/ARCHITECTURE.md) is
worth a look for the other half of the picture: a module that really is imported by other people,
where what sits at the top level is a deliberate, small public API.

## Install what you'll need

| Tool | You'll use it for | Install |
|---|---|---|
| `servo` | Generating and checking the wiring (chapter 13) | `go install github.com/okian/servo/v3/cmd/servo@latest` |
| Docker + `docker compose` | Running Postgres, Redis, NATS locally | [docs.docker.com](https://docs.docker.com/get-docker/) |
| `golangci-lint` | The CI lint step (chapter 18) | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |

You can actually skip installing `servo` globally — every command in this tutorial works equally
well as `go run github.com/okian/servo/v3/cmd/servo <command>`, and that's what the Makefile below
uses, so a fresh clone of the finished project works with nothing pre-installed except Go and
Docker.

## Write the Makefile

Add a `Makefile` at the root now. You'll be reaching for these commands from chapter 5 onward, and
it's one less thing to assemble under pressure later:

```makefile
.PHONY: up down test test-integration run run-gin run-grpc generate check

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

# The same service layer behind two other transports, built in chapters 11
# and 12. Neither binary exists yet; the targets go in now so the Makefile
# gets written once.
run-gin:
	go run ./cmd/ordersgin

run-grpc:
	go run ./cmd/ordersgrpc

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
