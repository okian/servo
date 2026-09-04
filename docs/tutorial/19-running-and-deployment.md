# 19. Running and deployment

Every earlier chapter ran this service with `go run ./cmd/orders` against infrastructure started by
hand. This chapter packages it properly: a `Dockerfile` that builds a small, static binary image,
and a `docker-compose.yml` that brings up the whole stack — Postgres, Redis, NATS, Jaeger, and now
the service itself — with one command. It closes with a full reference for every environment
variable the service reads, and the two real infrastructure problems most likely to show up the
first time you actually try this: a full disk, and a runtime image with no shell to debug from.

Everything here uses `cmd/orders`, the `net/http` transport. The
[Gin](11-gin-transport.md) and [gRPC](12-grpc-transport.md) binaries are built
and run identically — `make run-gin`, `make run-grpc`, or swap the path in the `Dockerfile`'s
`go build` line — and read the same environment variables.

## The Dockerfile

```dockerfile
# Built with the repo root as context, not this module's own directory —
# see docs/tutorial/19-running-and-deployment.md for why: this module's
# go.mod replaces github.com/okian/servo/v3 with a local path (../.., the
# servo repo itself), which only resolves if that path is actually present
# in the build context. A real project with a real, published dependency
# wouldn't need this; it's specific to this tutorial living inside servo's
# own repo.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
WORKDIR /src/examples/tutorial
RUN CGO_ENABLED=0 go build -o /out/orders ./cmd/orders

# The :nonroot variant runs as an unprivileged UID instead of root — free to
# switch to since both of this service's ports (8080, 8081) are well above
# 1024, the range only root can bind. Nothing else about the image changes.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/orders /orders
EXPOSE 8080 8081
ENTRYPOINT ["/orders"]
```

Two stages, doing two different jobs. The first, `build`, is a full `golang:1.27` image — over a
gigabyte, with a C toolchain, package caches, everything `go build` might need — and none of it
ends up in the final image. `CGO_ENABLED=0` matters here: it produces a statically-linked binary
with no dynamic dependency on `libc`, which is what makes the second stage possible at all. The
second stage starts from `gcr.io/distroless/static-debian12:nonroot` — not `scratch`, and not a
full `debian` or `alpine` image. `scratch` is literally empty; it has no CA certificate bundle,
which would break the OTLP exporter's ability to verify TLS the moment `OBS_OTLP_ENDPOINT` pointed at
anything other than an `--insecure` local collector. `distroless/static` ships exactly the CA
certificates and timezone data a static Go binary needs and nothing else — no shell, no package
manager, no coreutils. The `:nonroot` tag additionally runs as UID `65532` instead of root, for
free, since this service never needs a privileged port. The result:

Run from the repository root, not from `examples/tutorial/` — the build context has to be the
root for the same `replace` directive reason the Dockerfile's own top comment gives:

```
$ docker build -f examples/tutorial/deploy/Dockerfile -t servoorders:test .
...
Successfully built 91f867c77f24
Successfully tagged servoorders:test

$ docker images servoorders:test
IMAGE              ID             DISK USAGE   CONTENT SIZE   EXTRA
servoorders:test   91f867c77f24       62.4MB         17.8MB
```

62MB, most of which is the Go binary itself — everything OTel, Postgres, Redis, and NATS client
code included, statically linked. Confirm the nonroot switch actually took effect rather than
trusting the Dockerfile's comment:

```
$ docker inspect servoorders:test --format '{{.Config.User}}'
65532
```

## docker-compose.yml: the whole stack in one command

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: orders
      POSTGRES_PASSWORD: orders
      POSTGRES_DB: orders
    ports:
      - "5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U orders"]
      interval: 2s
      timeout: 2s
      retries: 15

  redis:
    image: redis:7-alpine
    # Persistence deliberately off: this Redis is a pure cache (chapter 6)
    # with nothing that isn't already durably in Postgres, so there's
    # nothing worth an RDB snapshot surviving a restart for, and skipping
    # it removes disk I/O this service doesn't need.
    command: redis-server --save ""
    ports:
      - "6379:6379"
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 2s
      timeout: 2s
      retries: 15

  nats:
    image: nats:2-alpine
    ports:
      - "4222:4222"
    healthcheck:
      test: ["CMD", "nc", "-z", "localhost", "4222"]
      interval: 2s
      timeout: 2s
      retries: 15

  jaeger:
    image: jaegertracing/all-in-one:1.60
    environment:
      COLLECTOR_OTLP_ENABLED: "true"
    ports:
      - "16686:16686" # web UI
      - "4318:4318"   # OTLP over HTTP -- what OTLPEndpoint points at

  # orders is the service itself, built from the same Dockerfile a reader
  # would build by hand (chapter 19). Its build context is the repo root,
  # not this directory — see deploy/Dockerfile's own top comment for why —
  # so this compose file must also be invoked with that in mind; `make up`
  # does this for you (see the Makefile) rather than a bare `docker compose
  # up` run from inside deploy/.
  #
  # postgres/redis/nats all get a real synchronous connection attempt
  # during this service's startup (each one's Init, run before servo
  # considers the graph ready) with no retry loop of its own — so unlike
  # Docker's default depends_on, which only waits for a container to have
  # started, orders waits for condition: service_healthy on all three, or
  # it would race a NATS/Postgres/Redis server that's still coming up and
  # fail on its first attempt. jaeger has no such requirement: span export
  # is asynchronous and just silently has nothing to send to until jaeger
  # is ready, so plain depends_on is enough there.
  #
  # No healthcheck is defined for orders itself: the runtime image is
  # gcr.io/distroless/static-debian12, which ships only the compiled
  # binary and CA certificates — no shell, no wget/curl/nc to run a check
  # with. Its own /healthz (see chapter 10) is still real and reachable
  # from the host at :8081/healthz; there just isn't a tool inside this
  # particular container to ask docker compose to poll it with.
  orders:
    build:
      context: ../../..
      dockerfile: examples/tutorial/deploy/Dockerfile
    environment:
      POSTGRES_DSN: postgres://orders:orders@postgres:5432/orders?sslmode=disable
      REDIS_ADDR: redis:6379
      NATS_URL: nats://nats:4222
      JWT_SECRET: dev-secret-do-not-use-in-production
      OBS_OTLP_ENDPOINT: jaeger:4318
    ports:
      - "8080:8080"
      - "8081:8081"
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
      nats:
        condition: service_healthy
      jaeger:
        condition: service_started
```

The comments on `redis` and `orders` above are in the real file, not added for this excerpt — the
rest of this section says the same things in prose, but it's worth being able to trust that every
code block in this tutorial is the literal file, not a paraphrase of it.

`orders` reaches the other four services by their service names (`postgres`, `redis`, `nats`,
`jaeger`) rather than `localhost` — Compose puts every service in the file on one Docker network
with DNS resolution by service name, which is also why this only works through `docker compose`
and not by running the image with a bare `docker run`. Its `depends_on` waits for
`condition: service_healthy` on postgres, redis, and nats specifically, not just
`service_started`: chapter 5, 6, and 7 each built their `Init` to fail on the first connection
attempt with no retry of its own, so a plain `depends_on` (which only waits for a container to
exist, not for whatever's inside it to be ready) would race a database that's still running
`initdb` and lose. `jaeger` gets the weaker `service_started` condition because nothing here waits
on it synchronously — span export is fire-and-forget, and a trace has somewhere to go the moment
Jaeger comes up, whenever that is.

`JWT_SECRET` is set directly in this file, in plain text, to a value that says exactly what it is.
That's fine for a compose file meant to be run locally and thrown away — it is not fine to carry
into anything real. See Do's and don'ts below.

`redis`'s `--save ""` and every healthcheck's short `2s` interval are both local-development
choices, not requirements — chapter 18 makes the same point about not carrying every local
convenience into CI, and it applies in reverse too: CI's actual `services:` block skips `--save`
entirely, since GitHub's runners are destroyed after every job anyway and there's nothing to
protect.

### Try it yourself

```
$ make up
...
 Container deploy-postgres-1 Healthy
 Container deploy-nats-1 Healthy
 Container deploy-redis-1 Healthy
 Container deploy-orders-1 Starting
 Container deploy-orders-1 Started

$ docker compose -f deploy/docker-compose.yml ps --format 'table {{.Name}}\t{{.Status}}'
NAME                STATUS
deploy-jaeger-1     Up 11 seconds
deploy-nats-1       Up 11 seconds (healthy)
deploy-orders-1     Up 8 seconds
deploy-postgres-1   Up 11 seconds (healthy)
deploy-redis-1      Up 11 seconds (healthy)
```

Every dependency reports healthy before `orders` even starts — exactly the ordering the
`condition: service_healthy` gates above exist to guarantee. Now exercise it the same way earlier
chapters did against `go run`, this time against the containerized binary:

```
$ curl -s http://localhost:8081/healthz
{"clean":true,"nodes":[{"name":"*example.com/servoorders/internal/repository/postgres.Store","status":"ok"},{"name":"*example.com/servoorders/internal/cache/redis.Cache","status":"ok"},{"name":"*example.com/servoorders/internal/broker/natsbroker.Publisher","status":"ok"}]}

$ curl -s -X POST http://localhost:8080/auth/login -H 'Content-Type: application/json' \
    -d '{"username":"alice","password":"password123"}'
{"token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1aWQiOiIxMTExMTExMS0xMTExLTExMTEtMTExMS0xMTExMTExMTExMTEiLCJ1c2VybmFtZSI6ImFsaWNlIiwiZXhwIjoxNzg3ODUwNzExLCJpYXQiOjE3ODc4NDcxMTF9.XrhaionnnG2GxOt6XEArPBSE43tRP1tsmdhtGwg3d-M"}
```

Create an order with that token and check the container's own logs — the two request logs, and
`notifier`'s log of the same event arriving back over NATS, should all show up, proving the whole
loop (API → Postgres → cache → NATS → `notifier`) runs inside this one container exactly as it did
under `go run`. Only `notifier` logs "order placed" — `OrderService.CreateOrder` itself stays quiet
on success and only logs on the two failure paths (chapter 8), so the third line below is
`notifier` receiving the event over NATS, not the service layer publishing it:

```
$ TOKEN=<the token above>
$ curl -s -X POST http://localhost:8080/orders -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' -d '{"item":"widget","quantity":3}'
{"id":"e7675d7d-7a70-476b-a244-9be32e5e5cb4","item":"widget","quantity":3,"status":"pending","created_at":"2026-08-27T16:11:51.324631541Z"}

$ docker compose -f deploy/docker-compose.yml logs orders --no-log-prefix
{"time":"2026-08-27T16:11:51.265878451Z","level":"INFO","msg":"request","method":"POST","path":"/auth/login","status":200}
{"time":"2026-08-27T16:11:51.326259011Z","level":"INFO","msg":"request","method":"POST","path":"/orders","status":201}
{"time":"2026-08-27T16:11:51.326523722Z","level":"INFO","msg":"order placed","order_id":"e7675d7d-7a70-476b-a244-9be32e5e5cb4","user_id":"11111111-1111-1111-1111-111111111111","item":"widget"}
```

And confirm the trace actually made it out of the container and into Jaeger. This can take a few
seconds — the OTel SDK's batch span processor (chapter 15) doesn't export on every request, only
once its batch timeout elapses — and `jaeger-all-in-one` reports itself as a service too, since it
instruments its own query API the same way any other OTel-instrumented service would:

```
$ curl -s http://localhost:16686/api/services
{"data":["servoorders","jaeger-all-in-one"],"total":2,"limit":0,"offset":0,"errors":null}
```

Open `http://localhost:16686` in a browser and search for the `servoorders` service to see the
actual span tree for that request. `make down` tears the whole stack back down, including its
volumes — there's no seed data worth keeping between runs.

## Environment variable reference

Every variable the service reads, gathered from the per-package `Config` types of chapter 3:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `HTTP_ADDR` | No | `:8080` | The public API — login, orders |
| `HTTP_ADMIN_ADDR` | No | `:8081` | `/healthz`, `/readyz`, `/metrics` — chapter 10, 15 |
| `POSTGRES_DSN` | **Yes** | — | e.g. `postgres://user:pass@host:5432/db?sslmode=disable` |
| `REDIS_ADDR` | **Yes** | — | `host:port`, no scheme |
| `NATS_URL` | **Yes** | — | e.g. `nats://host:4222` |
| `JWT_SECRET` | **Yes** | — | No default on purpose — chapter 3, 9 |
| `JWT_EXPIRY` | No | `1h` | Go duration string (`30m`, `2h`) |
| `OBS_LOG_LEVEL` | No | `info` | `debug`, `info`, `warn`, or `error` |
| `OBS_OTLP_ENDPOINT` | No | *(empty)* | `host:port`, no scheme — tracing is a no-op exporter until set |
| `RATE_LIMIT_RPS` | No | `50` | See chapter 16; a bare `resilience.Config{}` in a test omits this at its peril |
| `SESSION_RECENT` | No | `10` | How many recently-viewed orders a session keeps. The scope's linger window and Max are *not* here — both are constants in the spec file; see chapter 14 |

The four variables with no default (`POSTGRES_DSN`, `REDIS_ADDR`, `NATS_URL`, `JWT_SECRET`) are the
ones the service refuses to start without — that is what chapter 3's `required` tag is for. Each is
checked by its generated loader at the top of `New`, before anything is constructed, so a missing
one fails before `Run` is ever reached and the error names the variable. Nothing partially starts;
see [chapter 3](03-configuration.md#the-trade-this-design-makes) for what that costs compared to a
single up-front parse — and note the whole table above is `servo config --dir cmd/orders`'s output,
reformatted: the generator knows every variable the binary reads, so the deployment contract never
drifts from the code.

## Diagnostics

- **`docker build` (or `docker compose up --build`) fails partway through with `write ... no space
  left on device`, even though your Mac's own disk has plenty free** — Docker Desktop runs
  everything inside a Linux VM with its own separate virtual disk, sized independently of your
  actual host disk. Check the VM's view directly, not the host's:

  ```
  $ docker run --rm alpine df -h /
  Filesystem                Size      Used Available Use% Mounted on
  overlay                 294.7G    293.7G         0 100% /
  ```

  `docker system df -v` shows what's actually consuming that space — images, containers, and
  volumes, with per-item sizes. Dangling images and stopped containers from earlier builds
  (`docker image prune`, `docker container prune`) are always safe to reclaim. Named volumes take
  more judgment: they might belong to a completely different project on the same machine, and
  removing one forces whatever created it to rebuild from scratch next time. When the safe,
  unambiguous cleanup still isn't enough, the remaining options are freeing (or explicitly choosing
  to remove) whatever else is using the space, or growing the VM's disk allocation in Docker
  Desktop's own settings (Resources → Advanced) — which only helps if the actual host disk has room
  to grow into.
- **A container exits immediately with `initdb: error: could not create directory ... No space left
  on device`** — this is the same root cause as above, just surfacing inside Postgres's own startup
  instead of during `docker build`. Fix the underlying disk pressure first; retrying the container
  without doing so just fails the same way.
- **You want a shell inside the running `orders` container to poke around, and `docker exec -it
  deploy-orders-1 sh` fails with `OCI runtime exec failed: exec: "sh": executable file not found`**
  — this isn't a bug, it's `distroless/static`'s entire point: no shell, no package manager, nothing
  beyond the binary and CA certs. Debug from outside instead — `docker compose logs orders`,
  `curl` against its exposed ports, or (for something that genuinely needs a shell) temporarily
  swapping the final `FROM` line for a `debian:12-slim` base to get one, never as something that
  ships.
- **The `orders` service fails to start with a connection error even though `postgres`/`redis`/
  `nats` all show `Up` in `docker compose ps`** — check whether they show `(healthy)` too, not just
  `Up`. A container can be running long before whatever's inside it is actually accepting
  connections; this is exactly what `condition: service_healthy` above exists to wait for. If a
  service never turns healthy, check its own logs for why its health check keeps failing.
- **`docker build -f examples/tutorial/deploy/Dockerfile .` fails with a missing-module or
  can't-find-package error** — check the build context. It must be the repository root (the `.` at
  the end, run from the repo's top level), not `examples/tutorial/`, because `go.mod`'s `replace
  github.com/okian/servo/v3 => ../..` needs that path physically present in what gets sent to the
  Docker daemon. `make up` and the CI workflow (chapter 18) both already get this right; a bare
  `docker build` run from inside `examples/tutorial/deploy/` will not.

## Do's and don'ts

- **Do** build with a full SDK image and ship with a minimal one. The two-stage split here is what
  keeps a 62MB final image instead of shipping a full Go toolchain to production.
- **Do** reach for a `:nonroot` (or equivalent) variant of a minimal base image when nothing about
  the service actually needs root — it costs nothing here and removes a class of container-escape
  severity from "root in the container" to "an unprivileged UID."
- **Do** gate multi-container startup on health, not container existence — `depends_on: condition:
  service_healthy` here catches exactly the race a fail-fast, no-retry `Init` (chapter 5, 6, 7)
  would otherwise lose to.
- **Don't** treat this `docker-compose.yml`'s plaintext `JWT_SECRET` as anything other than a
  disposable local-dev convenience. A real deployment reads secrets from a secret manager or an
  orchestrator's own secret primitive (a Kubernetes `Secret`, an ECS task's `secrets:` block) —
  never commits them to a file that sits in version control.
- **Don't** reach for `scratch` reflexively just because it's the smallest possible base. It has no
  CA certificates, which silently breaks anything making an outbound TLS connection — this
  service's OTLP exporter among them, the moment `OBS_OTLP_ENDPOINT` points at something that isn't
  `--insecure`.
- **Don't** assume a healthy container is a ready one, or vice versa. `orders` itself deliberately
  has no Docker-level healthcheck at all (see the compose file's own comment) — its readiness is
  real and checkable at `:8081/healthz`, just not through a mechanism distroless has the tools to
  run from inside the container.

## Alternatives

- **Kubernetes instead of docker-compose.** `docker-compose.yml` here is a local-development
  convenience, not a production deployment target. A real Kubernetes deployment would translate
  each service into a `Deployment` (or a `StatefulSet` for Postgres), the health checks into
  `livenessProbe`/`readinessProbe` hitting the same `/healthz`/`/readyz` this service already
  exposes, and `JWT_SECRET` into a `Secret` mounted as an environment variable rather than written
  into a manifest. [Chapter 21](21-alternatives-and-further-reading.md) goes further into what
  changes at that scale.
- **A registry and a real image tag instead of a local-only build.** Nothing here pushes an image
  anywhere — chapter 18's `docker-build` job proves the image builds, and that's the limit of what
  this tutorial's CI has credentials to do. A real pipeline would tag with the commit SHA (or a
  semantic version) and push to a registry (ECR, GCR, Docker Hub, or a self-hosted one) as a
  release step.
- **`docker run` with a shared Docker network instead of `docker compose`.** Everything `docker
  compose up` does here — creating a network, resolving services by name, gating startup on health
  — is achievable with plain `docker network create` and individual `docker run --network ...
  --health-cmd ...` invocations. Compose exists to describe all of that declaratively in one file
  instead of a sequence of imperative commands; for a stack this size, it's a clear win, but it's
  worth knowing there's no magic underneath it.
- **A debug sidecar instead of swapping the base image.** Rather than temporarily rebuilding
  `orders` from a shell-having base to poke around inside it, Docker's own `docker debug` (and
  Kubernetes' `kubectl debug`) can attach an ephemeral container with a shell and standard tools
  into the same network/process namespace as a running distroless container, without changing the
  image that's actually deployed at all.

## Next

[Chapter 20: Troubleshooting](20-troubleshooting.md) — every diagnostic scattered across the last
seventeen chapters, gathered into one place organized by symptom instead of by layer.
