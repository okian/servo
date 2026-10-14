# Contributing

## Before you start

For anything beyond a small fix — a new marker, a resolution-rule change, a new CLI
command — open an issue first. It's cheaper to align on approach before writing code than after.
Typos, doc fixes, and small bug fixes can go straight to a PR.

## Dev workflow

Go 1.27+. From the repo root:

```
go build ./...
go vet ./...
go test -covermode=atomic -coverprofile=cover.out ./...
```

Every directory under `examples/` except `migrate` is a separate module (each `replace`s
`github.com/okian/servo/v3` with the repo root), so `./...` from the root never reaches them and
they need their own `go build ./...` / `go test ./...` inside those directories. This mirrors
[.github/workflows/go.yml](.github/workflows/go.yml) — match it and CI won't surprise you.

If you change anything that affects generated output, regenerate and check every example that has a
committed generated file — which is all of them except `diagnostics`, whose fixtures are
deliberately unresolvable and have none:

```
go run ./cmd/servo check --dir examples/basic
go run ./cmd/servo check --dir examples/scoped
go run ./cmd/servo check --dir examples/mocking
go run ./cmd/servo check --dir examples/http
go run ./cmd/servo check --dir examples/variants
go run ./cmd/servo check --dir examples/variants --tags=prod
go run ./cmd/servo check --dir examples/tutorial
```

`examples/variants` needs both invocations: a variant has its own generated file, and `check` only
inspects the one its build flags select.

Optionally wire the reference pre-commit hook, which runs `servo check` before every commit:

```
git config core.hooksPath githooks
```

In a *consumer* module the hook runs `go tool servo check`, the same form the `go:generate`
directive scaffolded by `servo init` uses — `go run github.com/okian/servo/v3/cmd/servo` fails
there on a missing `go.sum` entry, because requiring servo for the marker package alone doesn't put
the generator's own dependencies in their build list. Inside this repository there is no `tool`
directive and no injector at the root, so the same hook detects where it is and runs the example
checks above through `go run ./cmd/servo`, exactly as CI does.

## What CI checks

Four jobs, in [.github/workflows/go.yml](.github/workflows/go.yml), plus a separate
[tutorial workflow](.github/workflows/tutorial.yml):

- **build** — `go build`, `go vet`, `go test -race` at the root; then build and test each example
  module (`examples/scoped` at `-count=5`, and `examples/variants` under both build
  configurations), build `examples/diagnostics`, `servo check` basic, scoped, mocking, http,
  variants and the tutorial — the tutorial check needs no services, and the tutorial workflow
  runs it again for tutorial-only changes — and finally `servo doctor` and `servo-vet` (through
  `go vet -vettool`) over every example module, so the two commands a consumer runs outside
  `go generate` are exercised against real code and not only by their unit tests.
- **lint** — `golangci-lint` over *every* module, including `examples/tutorial`, and
  `examples/variants` a second time with `--build-tags=prod`, since files behind a build constraint
  are invisible to the default run.
- **docs** — builds the Jekyll site in `docs/` with the `github-pages` gem (the same gem set Pages
  itself runs) and asserts the key pages are non-empty and `search.json` parses. A broken layout or
  a Liquid typo used to reach the published site with nothing in front of it.
- **soak** — nightly only: 200 consecutive `-race` runs of `examples/scoped`, the real gate on
  scopes. The teardown race it exists for reproduced about once in 200 runs and passed `-count=5`
  every time.

The tutorial workflow lints, builds, vets, `gofmt`-checks, unit-tests and `servo check`s
`examples/tutorial`, then runs its integration suite against real Postgres/Redis/NATS and builds its
Docker image.

## Code expectations

- `gofmt`-formatted; `go vet` clean.
- New behavior needs a test. Coverage is tracked via Codecov on every PR.
- A new marker in `servo` is recognised by `servovet` automatically as long as it returns one of the
  existing option types (`Marker`, `HTTPOption`, `ScopeOption`, `TraceOption`). A new option *type*
  must be added to `servovet`'s `markerResultTypes`, and `TestMarkerDetectionCoversTheServoPackage`
  fails until it is. Without that, a call to the new marker in a file missing the `servoinject` tag
  is unflagged in the editor and compiles into the real binary, where the marker panics — which is
  the exact failure the analyzer exists to prevent.
  `servovet` holds the analyzer as an exported `Analyzer` so golangci-lint's module plugin system
  and `analysistest` can import it; `cmd/servo-vet` is the `singlechecker` binary around it.
- A page under `docs/` is part of the change, not a follow-up. The `docs` CI job only proves the
  site still builds — nothing checks that a claim on it is still true.
- If a change is breaking under the definition in [CHANGELOG.md](CHANGELOG.md) — exported API,
  CLI flag/exit-code contracts, or generated code's public method set — say so in the PR
  description; it affects whether the next release needs a `/vN` bump.

## License

Contributions are accepted under the project's [MIT license](LICENSE).
