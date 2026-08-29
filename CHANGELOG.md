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

## [Unreleased]

### Fixed
- **A successful `Acquire` racing `Shutdown` could hand back an instance that was already drained
  and stopped.** The entry loop evicted the moment it saw the scope's quit channel, whatever its
  reference count was, so no check an acquirer made could still hold by the time it returned.
  The creator path sampled `dead` before returning and lost that race about once in two hundred
  runs of the `examples/scoped` suite; the join path had no check at all. Against the pre-fix
  generated file, the two committed regression tests reproduce it in roughly a quarter of runs
  together, and a shared-key probe with the window widened by a millisecond hits it on nearly every
  goroutine. After the fix, 200 consecutive runs are green.

  A scope now drains the references outstanding when `Shutdown` arrived before tearing an instance
  down, bounded by one stop budget, so a counted reference is a promise the instance stays usable
  until it is given back. In the ordinary case there is nothing to wait for: reverse-level ordering
  means whatever depends on the scope has already been drained and its releases have landed. Past
  the bound the instance is torn down anyway and the entry is reported abandoned, so a caller who
  never releases still cannot hold shutdown open. An acquirer waiting to join when `Shutdown`
  begins is now told `ErrScopeClosed` immediately rather than waiting out a drain it is not part
  of.

  `examples/scoped` gains `TestSharedKeyAcquireAfterShutdown`, which covers the join path the
  existing test could not reach — it used a distinct key per goroutine, so every acquire took the
  creator path. Shutdown's per-entry budget multiplier grows by one to cover the new phase.
- **`servo generate` emitted code that did not compile when a scoped member type was named
  `Result`.** Entry field names were reserved, but the `drain<Field>`/`stop<Field>` method names
  derived from them were not, so a member named `Result` produced a `stopResult` method beside the
  entry's own `stopResult` field. `generate` exited 0 and `go build` failed, inside the one file
  users are told not to read. Two members named `Foo` and `StopFoo` collided the same way. Output
  is byte-identical for every graph without such a type.
- The same collision at App level: components named `Foo` and `StopFoo`, or one named
  `StartupReport`, also emitted a field and a method of one name. A node's variable name decides a
  `stop<F>` method and `<F>StopOnce`/`<F>StopResult` fields, and only the variable itself was ever
  reserved. Pre-existing, and the unfixed half of the same bug until now. Output is byte-identical
  for every graph without such a type.
- Those collisions — including the `A`/`Ctx`/`Err` case fixed in 3.1.0 — now have a test that
  builds a real module and compiles the output, since the failure being guarded against is a
  compile error and nothing else is an honest witness.
- An eviction is now counted in the same critical section that removes the entry. It was counted
  just after the lock was released, so `Live` could reach zero before `Evictions` was bumped —
  and `Live` reaching zero is the documented way to wait for a scope to go quiet. A test that
  waited that way and then read `Evictions` failed roughly once in two hundred runs.
- The **undeclared scope** message named the wrong interface in its pasteable `servo.Scoped` line
  whenever the accessor it found was not the scoped type's name plus an "s" — the prose said
  `CachePool` and the snippet said `Caches`, which does not exist.
- The **widening** message listed every accessor in the scope rather than only those the captured
  node is reachable from. With two `servo.Scoped` declarations sharing a key type, it named an
  accessor that does not lead to the type being held.

### Changed
- All four named scope diagnostics now carry the full needed-by chain. Only widening and
  cross-scope ever had it, and the 3.1.0 entry below claimed all four did until this release
  corrected it. The extractor-cycle chain leads to
  the scoped dependency rather than to the extractor, whose own position is already printed above
  it; the undeclared-scope chain comes from the traversal path that reached the type, since that
  check fires while candidates are still being selected and there is no finished graph to walk.
- The **undeclared scope** diagnostic no longer tells you to declare an accessor interface your
  package already has. The likeliest way to reach it is to have written the type, the `ScopeKey`
  method and the interface, and forgotten only the `servo.Scoped` line — in which case the
  suggested snippet was a redeclaration error. When an interface the generated accessor would
  satisfy already exists, the message names it and prints only the declaration.
- The **widening** diagnostic gives actionable advice for a node that is scoped only transitively.
  It used to say "depend on the scope's accessor interface" without naming one, and following that
  literally did not typecheck, because the accessor hands out the type at the scope's entrance and
  not the one being captured. It now names both.
- `examples/basic` no longer trips `errcheck` on two unchecked `app.Shutdown` calls in tests
  (`servo.Report` implements `error`).
- CI gains a `golangci-lint` job covering every module, root and examples. Nothing enforced the
  linter before, which is how the two `errcheck` failures above shipped.
- CI gains a nightly `soak` job running `examples/scoped` 200 consecutive times under `-race`. That
  is the stated gate on scopes, and it had only ever been asserted in a commit message. Pull
  requests keep the `-count=5` run. The teardown race fixed above reproduced roughly once in 200
  runs and passed `-count=5` every time, so the distinction is not academic.
- `examples/scoped` gains a `README.md`. It was the only scope surface without one, and it is the
  module the feature is gated on.
- `examples/diagnostics` gains a `noscopekey/` fixture for the reverse undeclared case — a
  `servo.Scoped` declaration whose type has no `ScopeKey` method. Its wording was pinned only by a
  resolver unit test, and it is the half a new user meets first.

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
- Four generate-time diagnostics — **widening** (a singleton
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
