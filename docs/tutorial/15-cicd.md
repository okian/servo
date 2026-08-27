# 15. CI/CD

Every check so far has been something you ran by hand: `go build`, `make test`, `make
test-integration`, `docker build`. That's fine while you're the only one running them, but it stops
scaling the moment a second person opens a pull request — nothing forces them to remember all four
commands, in the right order, before merging. This chapter turns them into a GitHub Actions
workflow that runs automatically on every push and pull request, so a broken build or a stale
`servo_gen.go` fails loudly in CI instead of quietly in someone's local checkout.

## Why a second workflow file, not a bigger one

The servo repository already has `.github/workflows/go.yml`, which builds, vets, and tests the
root module plus the other `examples/*` modules on every push. `examples/tutorial` isn't added to
it. It gets its own `.github/workflows/tutorial.yml` instead, for a reason that'll matter more as a
real project's test suite grows: this module's integration tests need Postgres, Redis, and NATS as
service containers, and its Docker build takes real time. Bundling that into `go.yml` would mean
every change anywhere in the servo repository — a comment fix in `breaker/`, a typo in the root
README — waits on a Docker build it has nothing to do with.

```yaml
on:
  push:
    branches: [ master ]
    paths:
      - 'examples/tutorial/**'
      - 'docs/tutorial/**'
      - '.github/workflows/tutorial.yml'
  pull_request:
    branches: [ master ]
    paths:
      - 'examples/tutorial/**'
      - 'docs/tutorial/**'
      - '.github/workflows/tutorial.yml'
```

The `paths:` filter is what makes this safe to keep separate: the workflow simply doesn't run
unless something under `examples/tutorial/`, `docs/tutorial/`, or the workflow file itself changed.
A change to `breaker/breaker.go` at the repository root never triggers it. This is the same
trade-off a monorepo with many services eventually has to make deliberately — one fast, universal
pipeline for the parts everyone touches, and narrower, heavier pipelines path-filtered to the parts
that need them.

## Four jobs, each answering one question

```mermaid
flowchart LR
    lint["lint<br/>golangci-lint"]
    build["build-and-unit-test<br/>build, vet, gofmt, unit tests, servo check"]
    integ["integration-test<br/>postgres + redis + nats services<br/>real driver tests"]
    docker["docker-build<br/>full multi-stage image build"]

    lint & build & integ & docker
```

All four run in parallel — none depends on another finishing first, since each answers a different
question and a failure in one shouldn't hide a failure in the others. (`servo check`, which does
depend on `go build ./...` actually working, still runs in the same job as build/vet rather than a
separate one — sequencing steps *within* a job is fine and normal; it's the four *jobs* that don't
need to depend on each other.)

### `lint`

```yaml
lint:
  name: Lint
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.25'
    - uses: golangci/golangci-lint-action@v6
      with:
        working-directory: examples/tutorial
```

`golangci-lint-action` handles installing the right version and reading the module's own
`go.mod` — nothing tutorial-specific here beyond `working-directory`, since `examples/tutorial` is
a separate Go module from the repository root and needs to be linted as one.

`examples/tutorial/.golangci.yml` matters more than its size suggests. golangci-lint's default
rule set enables `errcheck`, which flags every unchecked error return — including things like
`defer conn.Drain()`, `tx.Rollback(ctx)` in a deferred no-op, or `json.NewEncoder(w).Encode(...)`
writing to an HTTP response that's already been written to once. None of those have a meaningful
error-handling action to take (the connection is shutting down anyway; the transaction is already
lost; there's no way to send a *different* response after the first `Write`), so this config
excludes exactly those specific calls by name, plus `errcheck` entirely inside `_test.go` files
(test cleanup code failing to check a `Stop`/`Unsubscribe`/`Flush` error isn't the same risk as
production code doing it — the test's own assertions already caught anything that mattered). This
isn't a blanket "disable errcheck" — running `golangci-lint run ./...` with zero config here
reports 24 issues, all in this same handful of idiomatic-to-ignore shapes; a *real* unchecked error
this config doesn't already know about still gets flagged.

### `build-and-unit-test`

```yaml
build-and-unit-test:
  name: Build, vet, and unit test
  runs-on: ubuntu-latest
  defaults:
    run:
      working-directory: examples/tutorial
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.25'

    - name: Build
      run: go build ./...

    - name: Vet
      run: go vet ./...

    - name: gofmt
      run: |
        diff <(gofmt -l .) <(echo -n "")

    # No TEST_POSTGRES_DSN/TEST_REDIS_ADDR/TEST_NATS_URL here — every
    # integration test (see e.g. postgres/postgres_test.go) skips itself
    # when its env var isn't set, rather than failing, so this step only
    # ever exercises the mock-backed unit tests. See
    # docs/tutorial/14-testing-strategy.md for why the split matters:
    # this job has no services: block at all, so a test that forgot to
    # skip would fail here with a connection error, not silently pass.
    - name: Unit test
      run: go test ./...

    # The reference check every consumer of servo should run in CI:
    # verifies cmd/orders/servo_gen.go still matches what `servo
    # generate` would produce right now, so a constructor signature
    # change without a re-run of `servo generate` fails the build
    # instead of shipping a stale generated file. Run from the repo
    # root, same as go.yml does for examples/basic and
    # examples/mocking, since cmd/servo is a root-module binary.
    - name: servo check
      working-directory: .
      run: go run ./cmd/servo check --dir examples/tutorial
```

Two things worth noticing. First, `gofmt -l .` lists files that *aren't* formatted correctly — an
empty list means everything's clean — so the step diffs its output against an empty string rather
than checking `gofmt`'s exit code, which is 0 regardless of whether it found anything to list.
Second, `servo check` runs with `working-directory: .`, overriding the job's own default of
`examples/tutorial`, because `cmd/servo` is a binary that lives in the *root* module, not this
one — the same pattern `go.yml` already uses for `examples/basic` and `examples/mocking`. This step
is the CI enforcement of the warning chapter 11 raised when introducing `servo generate`: if
`cmd/orders/servo_gen.go` doesn't match what `servo generate` would produce right now — because a
constructor's signature changed and nobody re-ran it — this fails the build instead of shipping a
stale generated file.

`go test ./...` here runs with none of the `TEST_POSTGRES_DSN`/`TEST_REDIS_ADDR`/`TEST_NATS_URL`
variables set, and this job declares no `services:` block at all. That's deliberate, and it's a
real safety property, not just tidiness: if a test in `postgres/` or `redis/` ever forgot to check
its environment variable and skip, it would fail *here*, with a connection error, rather than
silently passing by accident because some service happened to be reachable. Chapter 14 calls this
out as a real diagnostic to watch for — this job is what makes it visible in the first place.

### `integration-test`

```yaml
integration-test:
  name: Integration test (real Postgres/Redis/NATS)
  runs-on: ubuntu-latest
  defaults:
    run:
      working-directory: examples/tutorial
  services:
    postgres:
      image: postgres:16-alpine
      env:
        POSTGRES_USER: orders
        POSTGRES_PASSWORD: orders
        POSTGRES_DB: orders
      ports:
        - 5432:5432
      options: >-
        --health-cmd pg_isready
        --health-interval 5s
        --health-timeout 5s
        --health-retries 10
    redis:
      image: redis:7-alpine
      ports:
        - 6379:6379
      options: >-
        --health-cmd "redis-cli ping"
        --health-interval 5s
        --health-timeout 5s
        --health-retries 10
    nats:
      image: nats:2-alpine
      ports:
        - 4222:4222
      options: >-
        --health-cmd "nc -z localhost 4222"
        --health-interval 5s
        --health-timeout 5s
        --health-retries 10
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-go@v5
      with:
        go-version: '1.25'

    # GitHub Actions publishes service container ports to the runner's
    # own localhost, so these are the same values docker-compose.yml
    # uses for local dev — no service-discovery translation needed
    # between the two.
    - name: Integration test
      env:
        TEST_POSTGRES_DSN: postgres://orders:orders@localhost:5432/orders?sslmode=disable
        TEST_REDIS_ADDR: localhost:6379
        TEST_NATS_URL: nats://localhost:4222
      run: go test ./... -v
```

Every `options: --health-cmd ...` here matters more than it might look. GitHub Actions runs a
service container's health check *before* letting the job's own steps start — a service declared
without one is merely started, with no guarantee it's actually accepting connections yet by the
time `Integration test` runs. None of `postgres.Store.Init`, `redis.Cache.Init`, or
`natsbroker.Publisher.Init` retry a failed connection (chapter 5, 6, and 7 each built them to fail
fast instead), so a race here wouldn't be a flaky occasional failure — the very first test in each
package would reliably fail on a cold runner. `pg_isready` and `redis-cli ping` ship inside their
respective official images, so no extra installation is needed; `nc -z localhost 4222` stands in
for NATS the same way it does in `deploy/docker-compose.yml`'s own healthcheck, since the
`nats:2-alpine` image ships busybox's `nc` but nothing NATS-specific for health checking. This is
the exact same problem chapter 16's `depends_on: condition: service_healthy` solves for local
`docker compose up` — CI and local dev hit the same race for the same underlying reason, and both
fix it the same way.

Notice, too, that this job doesn't bother reproducing `docker-compose.yml`'s
`redis-server --save ""` tuning. That flag exists locally to avoid RDB snapshots competing for disk
with everything else running on a developer's machine; a GitHub-hosted runner is a disposable VM
that's destroyed the moment the job ends, so there's no accumulating disk pressure to avoid in the
first place. Carrying local-environment tuning into CI unexamined is worth resisting — some of it
won't apply, and some of it (a health check, for instance) turns out to matter in both places for
different reasons.

### `docker-build`

```yaml
docker-build:
  name: Docker build
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4

    # Built with the repo root as context, not examples/tutorial/ — see
    # deploy/Dockerfile's own top comment: this module's go.mod replaces
    # github.com/okian/servo/v3 with ../.., which only resolves if that
    # path is actually present in the build context. Not pushed: no
    # registry credentials are available to this workflow, and a
    # tutorial has no image to publish anywhere. This step exists to
    # catch a Dockerfile that's silently drifted from a source change
    # (a new dependency, a renamed package) before a reader hits it.
    - name: Build image
      run: docker build -f examples/tutorial/deploy/Dockerfile -t servoorders:ci .
```

This builds the same image chapter 16 builds and runs locally, from the same repo-root context, for
the same reason (the `replace` directive in `examples/tutorial/go.mod` needs `../..` — the servo
repository itself — inside the build context). It doesn't push anywhere: this workflow has no
registry credentials configured, and a tutorial has nothing to publish. Its job is narrower than
"produce a deployable artifact" — it exists to catch a Dockerfile that's silently drifted from a
source change (a new dependency the multi-stage build doesn't know to fetch, a renamed package) as
part of normal CI, rather than the first time someone actually tries to deploy.

## Verifying a workflow you can't fully dry-run

There's an honest limit here: actually exercising this workflow end to end means pushing it and
watching GitHub Actions run it, which isn't something to do from inside a tutorial write-up. What
*is* verifiable without that is whether the YAML is well-formed and semantically valid GitHub
Actions syntax — job structure, service container options, expression syntax — which
[actionlint](https://github.com/rhysd/actionlint) checks directly:

```
$ docker run --rm -v "$PWD":/repo -w /repo rhysd/actionlint:latest .github/workflows/tutorial.yml
$ echo $?
0
```

No output and a `0` exit code is actionlint's way of saying the workflow is clean. Beyond syntax,
every individual command this workflow runs was already run for real, outside of CI, earlier in
this tutorial: `go build`/`go vet`/`go test` in chapter 14, `servo check` in chapter 11, the exact
`docker build` invocation in chapter 16. The workflow's job is to run those same, already-proven
commands automatically — it doesn't introduce a single command that hasn't already been verified to
work on this exact codebase.

## Diagnostics

- **The workflow doesn't trigger on a PR that clearly touches `examples/tutorial/`** — check the
  `paths:` filter against the actual changed files; a change only under a directory not listed
  there (a root-level `go.mod` bump, say) won't trigger it even if it happens to affect this module
  too.
- **`integration-test` fails immediately on every test, not intermittently** — almost always a
  service's `ports:` mapping or a test's `TEST_*` variable disagreeing on the port number, not an
  actual connectivity problem. Double check `5432`/`6379`/`4222` appear consistently on both sides.
- **A service container's health check never passes, and the job times out** — confirm the
  `--health-cmd` binary actually exists inside that exact image tag. `pg_isready` and `redis-cli`
  are safe bets for the official Postgres/Redis images; a slimmer or custom image might not have
  them, the same way `nats:2-alpine` has no NATS-aware health-check binary of its own.
- **`servo check` fails only in CI, never locally** — someone ran `servo generate` locally,
  edited the generated output by hand afterward, and committed both. `servo check`'s whole purpose
  is to catch exactly this, so treat it as correct and regenerate rather than adjusting the check.
- **`docker-build` fails with a missing-module error that `go build` locally doesn't reproduce** —
  check whether `.dockerignore` (or its absence) is excluding something the multi-stage build's
  `COPY . .` needs; the build context here is the whole repository, not just this module.

## Do's and don'ts

- **Do** path-filter a heavier, slower workflow away from the fast pipeline everyone's changes run
  through, rather than making everyone wait for a Docker build they didn't ask for.
- **Do** let GitHub Actions' own `--health-cmd` gate service readiness instead of hand-rolling a
  `sleep 10` or a bash polling loop — it's declarative, fails with a clear timeout message, and is
  one less thing to get subtly wrong.
- **Do** keep the unit-test job free of any `services:` block, deliberately, so a test that should
  skip without real infrastructure but doesn't gets caught immediately instead of masked.
- **Do** run `servo check` in CI even though it's fast enough to feel unnecessary — the failure
  mode it catches (a hand-edited or stale generated file) is exactly the kind of thing a reviewer
  skimming a diff is likely to miss.
- **Don't** push a Docker image from CI until there's an actual registry and a real credentials and
  tagging strategy behind it. A `docker build` that only ever proves the image *builds* is a
  legitimate, complete milestone on its own — don't add a `docker push` just because the job is
  already there, with nowhere real for the image to go.
- **Don't** copy this workflow's `services:` block wholesale onto a project whose components *do*
  retry failed connections. The health-check gating here is a direct response to this codebase's
  fail-fast `Init` methods; a component with its own retry/backoff might not need it at all.

## Alternatives

- **A single combined workflow instead of four separate jobs.** Splitting `lint`,
  `build-and-unit-test`, `integration-test`, and `docker-build` into separate jobs means GitHub
  Actions runs them concurrently (on separate runners) and reports each pass/fail independently in
  a PR's checks list — a lint failure is immediately distinguishable from a Docker build failure.
  One combined job would use a single runner sequentially, which is simpler to read top-to-bottom
  but slower overall and less precise about which concern actually broke.
- **`actions/cache` for the Go module and build cache.** Not used here, since a tutorial workflow
  that only runs on changes to one small module doesn't run often enough for the caching to pay for
  its own added complexity (a cache key strategy, occasional stale-cache debugging). A workflow that
  runs on every push to a large, frequently-changed module would likely find the trade worthwhile.
- **testcontainers-go instead of a `services:` block** (see also chapter 14's alternatives) — would
  let `integration-test` collapse into the same job as unit tests, since the containers would be
  started by the test process itself rather than declared at the job level. Trades a `services:`
  block most Go developers can read at a glance for a dependency on the Docker socket being
  available to the test binary, which GitHub-hosted runners do provide but not every CI environment
  does.
- **GitLab CI, CircleCI, or another CI system.** The shape of this pipeline — lint, build/vet,
  fast tests without infrastructure, slower tests with real service containers, an image build —
  isn't GitHub-Actions-specific. GitLab CI's `services:` keyword and CircleCI's Docker executor
  both support the same service-container pattern; only the YAML dialect changes.
- **Dependabot or Renovate for dependency updates.** Neither is configured here. A real project
  depending on this many third-party modules (pgx, go-redis, nats.go, the OTel SDK, gobreaker)
  would normally want automated PRs for version bumps rather than noticing drift manually.

## Next

[Chapter 16: Running and deployment](16-running-and-deployment.md) — the `Dockerfile` and
`docker-compose.yml` this chapter's `docker-build` job and chapter 14's `make up` both depend on,
and a full environment variable reference for running the service anywhere.
