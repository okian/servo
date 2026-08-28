# Changelog

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Versioning

servo follows [Semantic Versioning](https://semver.org/): MAJOR.MINOR.PATCH. Because this is a Go
module, a MAJOR bump also changes the import path — `github.com/okian/servo/vN` — per
[Go's own modules convention](https://go.dev/doc/modules/major-version). That convention is the
whole point: it's what stops `go get -u`, or any caret/latest-compatible version constraint, from
ever silently handing an existing consumer a backwards-incompatible release under a path they
already depend on. A release's tag and its import path's version suffix always match.

**Breaking (forces the next `/vN` bump):** any change to `servo` or `servotest`'s exported API —
signatures, types, or documented behavior, including a marker function starting or ceasing to
panic; any change to a CLI command's flags, subcommands, or exit-code contract that an existing
`servo check` CI step might depend on; any change to generated code's public method set (`New`,
`Run`, `Shutdown`, `Health`, `Ready`, `Graph`, `Report` and their signatures).

**Not breaking:** regenerating a stale `servo_gen.go` is always expected and cheap, so a change to
generated code's internal shape, field names, or formatting is not itself a breaking change as
long as the public methods above keep their signatures — consumers regenerate, they don't hand-edit
that file. Also not breaking: new capability interfaces, new CLI subcommands or flags, improved
diagnostic wording, or a case that used to be a diagnostic now resolving successfully.

## [3.2.0] - 2026-08-28

### Changed
- **Go 1.27 is now the floor.** `go.mod` declares `go 1.27.0`, raised from `go 1.25.0`, as do all
  five example modules. What a consumer still on 1.25 or 1.26 sees depends on their `GOTOOLCHAIN`:
  under the default `auto`, Go downloads and switches to 1.27 for them and the upgrade is
  invisible; under `local` or a pinned toolchain, `go build` reports that the module requires
  `go >= 1.27.0`. Builds already pinned to 3.1.x keep working either way. This is a minor bump,
  not a major one: it changes no
  exported API in `servo` or `servotest`, no CLI flag, subcommand or exit code, and nothing in
  generated code's public method set, which is what this file's Versioning section defines as
  breaking. It is called out here because it is the one change in this release a consumer can
  feel.
- The CI matrix is a single `1.27` entry, down from `1.25, 1.26, 1.27`. Those entries pinned
  `GOTOOLCHAIN=local` deliberately, so with the floor at 1.27 they could no longer build the
  module at all — and they were testing toolchains no consumer can now be on. The matrix is kept
  rather than flattened so the next release is one line to add. Two `if: matrix.go-version !=
  '1.25'` guards around `examples/mocking` are gone with them.
- `examples/tutorial` uses the standard library's `uuid` package, new in Go 1.27, instead of
  `github.com/google/uuid`, which it no longer requires directly. The four symbols it used —
  `UUID`, `New`, `Parse`, `MustParse` — carry identical signatures across, so this is an import
  swap and not a behavior change. Every chapter listing that showed the old import was updated to
  match.
- `cmd/orders/servo_gen.go` regenerates with one changed line: dropping an import moved
  `session.New` from `session/session.go:60:6` to `59:6`, and the resolved-graph comment records
  provider positions. `docs/tutorial/12-scoped-instances.md` quotes that position twice and was
  updated with it. `examples/basic`, `examples/mocking` and `examples/scoped` regenerate
  byte-identically.
- Internally, `errors.AsType` (Go 1.26) replaces the `errors.As` out-parameter in
  `internal/resolve`. No behavior change.

## [3.1.0] - 2026-08-28

### Added
- **Scoped instances.** A type declaring a `ScopeKey` method gets one instance per key instead of
  one per process: everyone presenting the same key shares it, and it is drained, stopped and
  evicted once the last holder lets go and a linger window closes. The instance map, the reference
  counting, the timer and the per-instance `Init`/`Run`/`Drain`/`Flush`/`Stop` are all generated.
  See [Scoped instances](https://okian.github.io/servo/reference/scopes.html) and
  [`examples/scoped`](examples/scoped).
- `servo.Scoped[T, I](...)`, `servo.Linger(d)` and `servo.Max(n)` markers, plus `servo.ScopeOption`,
  `servo.DefaultLinger`, `servo.DefaultMax`, `servo.ScopeStats`, `servo.LingerWindow`,
  `servo.LingerOverride`, and the errors `servo.ErrNoScopeKey`, `servo.ErrNoLifetime`,
  `servo.ErrScopeFull` and `servo.ErrScopeClosed`.
- `servotest.Linger(t, d)`, which shrinks every scope's linger window for one test the way
  `servotest.Timeout` shrinks the stop budget.
- Four generate-time diagnostics, each with the full needed-by chain: **widening** (a singleton
  depending on a scoped node — the one that makes this worth generating rather than hand-writing),
  **cross-scope** (nested scopes, rejected on purpose), **extractor cycle** (a `ScopeKey` parameter
  that is itself scoped), and **undeclared scope** (a `ScopeKey` with no `servo.Scoped`, or the
  reverse). Four new fixtures in [`examples/diagnostics`](examples/diagnostics) reproduce them.
- `cmd/servo-vet` gains a second rule: a `ScopeKey` method whose body can reach its own receiver.
  Generated code calls that method on a typed nil, and no signature can express "never
  dereferences the receiver", so it is checked rather than assumed. `servo generate` makes the same
  check.
- `servo graph` reports scope attribution in all four formats; `servo explain` reports a node's
  lifetime and, for a scoped node, its level within its own scope. `explain --json` gains
  `lifetime`, and `scope` for a scoped node.
- Further scope diagnostics, each covering a case that would otherwise be silently wrong or emit a
  generated file that does not compile: a node holding a scope key it is not in scope for, a node
  two scopes both claim, a scoped type or accessor declared as a `servo.Root`, a `servo.Bind` or
  `servo.Override` naming an accessor interface, and a non-pointer scoped type.
- More scope diagnostics from a second review round: a constructor that produces an accessor
  interface (dead code resolution would never call), and a `ScopeKey` extractor that takes its own
  scope's accessor (unbounded recursion). An extractor taking *another* scope's accessor is now
  correctly allowed — it is the documented way out of a cross-scope dependency.
- A panic in a scoped constructor or `Init` is recovered, rolled back, and returned from `Acquire`
  as an error. Uniform on both the sequential and concurrent `Init` paths: a panic from an
  `errgroup` goroutine would otherwise take the process down for one failed request.
- **Fixed (pre-existing, not scope-related):** generated code failed to compile when a component
  type was named `A`, `Ctx` or `Err`, because `New`'s own locals collide with the variable names
  derived from those types. Output is byte-identical for every graph without such a type.
- `servo.ScopeStats.Failures` counts evictions whose teardown did not come out clean. An instance
  evicted mid-life has no `Report` to appear in, so without it a component that consistently fails
  to stop left no trace anywhere.

### Changed
- `servo.GraphNode` gains a `Scope` field and `servo.Graph` a `Scopes` field, both `omitempty`, so
  `servo graph --format=json` is byte-identical for an app that declares no scopes. Consumers
  reading that schema should expect the two new keys once scopes are in use. This is additive: it
  breaks only code constructing a `servo.GraphNode` with an unkeyed composite literal.
- An app with no `servo.Scoped` declaration generates byte-identical output to 3.0.1. The evidence
  is that `examples/basic` and `examples/mocking` regenerate with no diff against their committed
  3.0.1 files, and `internal/emit/testdata/golden/fullapp.go.golden` is unchanged.
  (`examples/tutorial` is *not* evidence for this: it now declares a scope, so its generated file
  changed on purpose.)

## [3.0.1] - 2026-08-28

### Changed
- No changes to `servo`/`servotest`'s exported API, the CLI, or generated code — this release is
  CI and repository maintenance only.
- Bumped `actions/checkout` and `github/codeql-action` to their current major versions across every
  workflow (both were past or nearing deprecation).
- Added `CODE_OF_CONDUCT.md`, issue templates, and a pull request template.
- Expanded test coverage from 94.1% to 98.7% of statements; added `codecov.yml` to exclude
  `examples/migrate`'s deliberately-uncalled fixture code from the ratio.

## [3.0.0] - 2026-08-27

### Changed
- **Breaking — module path**: `github.com/okian/servo/v2` → `github.com/okian/servo/v3`. This
  release shares no API with anything previously published under `/v2`, for the reason below.

### Added
- Complete rewrite: build-time dependency resolution and code generation, replacing the runtime
  lifecycle sequencer previously published under `github.com/okian/servo` and
  `github.com/okian/servo/v2` (global registry, hand-maintained `order int`,
  `Initialize(ctx) error` with no parameters). See [README.md](README.md) for usage and
  [ARCHITECTURE.md](ARCHITECTURE.md) for how it's built.
- `servo migrate` reads the old `Register(X{}, N)` calls and emits a starting skeleton for the new
  spec-file format, plus a report flagging duplicate order values for manual review.
