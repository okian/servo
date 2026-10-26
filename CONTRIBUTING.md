# Contributing

## Before you start

For anything beyond a small fix — a new marker, a resolution-rule change, a new CLI
command — open an issue first. It's cheaper to align on approach before writing code than after.
Typos, doc fixes, and small bug fixes can go straight to a PR.

## Dev workflow

servo supports the two most recent Go releases, matching Go's own support window — Go 1.26 and
1.27 today. go.mod's `go` line is the older of the two, and CI runs both. From the repo root:

```
go build ./...
go vet ./...
go test -race ./...
```

Every directory under `examples/` except `migrate` is a separate module (each `replace`s
`github.com/okian/servo/v3` with the repo root), so `./...` from the root never reaches them and
they need their own `go build ./...` / `go test ./...` inside those directories. This mirrors
[.github/workflows/go.yml](.github/workflows/go.yml) — match it and CI won't surprise you.

### Generated output

If you change anything that affects generated output, regenerate it rather than editing it: the
emitter's golden files first, then every example with committed generated files — all of them
except `diagnostics`, whose fixtures are deliberately unresolvable and have none:

```
UPDATE_GOLDEN=1 go test ./internal/emit/...
go run ./cmd/servo generate --dir examples/basic
go run ./cmd/servo generate --dir examples/scoped
go run ./cmd/servo generate --dir examples/mocking
go run ./cmd/servo generate --dir examples/http
go run ./cmd/servo generate --dir examples/tutorial
go run ./cmd/servo generate --dir examples/variants
go run ./cmd/servo generate --dir examples/variants --tags=prod
```

Read the diff — a golden or generated file that changed when you didn't mean it to is the bug —
then check that every committed file matches what `servo generate` would write:

```
go run ./cmd/servo check --dir examples/basic
go run ./cmd/servo check --dir examples/scoped
go run ./cmd/servo check --dir examples/mocking
go run ./cmd/servo check --dir examples/http
go run ./cmd/servo check --dir examples/variants
go run ./cmd/servo check --dir examples/variants --tags=prod
go run ./cmd/servo check --dir examples/tutorial
```

`examples/variants` needs both invocations: a variant has its own generated file, and `generate`
and `check` only see the one their build flags select.

### The pre-commit hook

Optionally wire the reference pre-commit hook, which runs `servo check` before every commit:

```
git config core.hooksPath githooks
```

It checks what is being committed, not the working tree: anything not staged, untracked files
included, is set aside for the check and put back exactly as it was afterwards, so a commit that
stages a constructor change but not its regenerated `servo_gen.go` is refused even when the working
tree is consistent.

In a *consumer* module the hook runs `go tool servo check`, the same form the `go:generate`
directive scaffolded by `servo init` uses — `go run github.com/okian/servo/v3/cmd/servo` fails
there on a missing `go.sum` entry, because requiring servo for the marker package alone doesn't put
the generator's own dependencies in their build list. Inside this repository there is no `tool`
directive and no injector at the root, so the same hook detects where it is and runs the example
checks above through `go run ./cmd/servo`, exactly as CI does.

## What CI checks

[.github/workflows/go.yml](.github/workflows/go.yml) runs on every push and pull request:

- **test** — `go build`, `go vet` and `go test -race` for the root module, on both supported Go
  versions. `cmd/servo`'s suite, most of the test time, runs in four shards beside a fifth job for
  every other package; a **coverage** job merges their profiles and uploads one to Codecov.
- **examples** — builds and tests each example module on every Go version its go.mod allows
  (`examples/scoped` at `-count=5`, `examples/variants` under both build configurations, the
  tutorial on 1.27 only), builds `examples/diagnostics`, runs `servo check` on every example, and
  finally `servo doctor` and `servo-vet` (through `go vet -vettool`) over every example module, so
  the two commands a consumer runs outside `go generate` are exercised against real code and not
  only by their unit tests.
- **lint** — `golangci-lint` over *every* module, including `examples/tutorial`, and
  `examples/variants` a second time with `--build-tags=prod`, since files behind a build constraint
  are invisible to the default run.
- **tidy** — `gofmt` over every committed Go file, and `go mod tidy -diff` in every module.
- **typos**, **actionlint**, **links** — spelling, with the allowlist in [typos.toml](typos.toml);
  the workflow files themselves; and every relative link and `#fragment` in the Markdown, checked
  offline, plus a check that no link in `docs/` has its text wrapped across lines, which the
  published site cannot follow.
- **docs** — builds the Jekyll site in `docs/` with the `github-pages` gem set pinned in
  `docs/Gemfile.lock` (the gems Pages itself runs) and asserts the key pages are non-empty, that
  `search.json` parses, and that no page links to a `.md` file. A broken layout or a Liquid typo
  used to reach the published site with nothing in front of it.
- **determinism** — regenerates every example on Linux, macOS and Windows from a moved checkout, a
  moved module cache, vendored dependencies and a `go.work`, and fails if any byte differs from the
  committed files ([.github/scripts/determinism.sh](.github/scripts/determinism.sh) runs locally
  too).
- **soak** and **canary** — nightly only. The soak is 200 consecutive `-race` runs of
  `examples/scoped`, the real gate on scopes: the teardown race it exists for reproduced about once
  in 200 runs and passed `-count=5` every time. The canary runs the root suite and every example's
  `servo check` on the newest Go release and on Go's development branch, so a Go release that breaks
  servo is found before its users find it.

Alongside it:

- the [tutorial workflow](.github/workflows/tutorial.yml) lints, builds, vets, `gofmt`-checks,
  unit-tests and `servo check`s `examples/tutorial`, then runs its integration suite against real
  Postgres/Redis/NATS and builds its Docker image — whenever the tutorial, or any root package it
  builds against, changes;
- [vulncheck.yml](.github/workflows/vulncheck.yml) runs `govulncheck` over every module nightly
  and whenever a `go.mod` or `go.sum` changes;
- [CodeQL](.github/workflows/codeql-analysis.yml) analyzes the Go code and the workflows.

Every job runs with a read-only token unless it says why it needs more, and every action is pinned
to a commit SHA; Dependabot proposes the updates. Releases are cut through
[cut-release.yml](.github/workflows/cut-release.yml), described in [RELEASING.md](RELEASING.md).

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
  site still builds and its links resolve — nothing checks that a claim on it is still true.
- If a change is breaking under the definition in [CHANGELOG.md](CHANGELOG.md) — exported API,
  CLI flag/exit-code contracts, or generated code's public method set — say so in the PR
  description; it affects whether the next release needs a `/vN` bump.

## License

Contributions are accepted under the project's [MIT license](LICENSE).
