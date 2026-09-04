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

**Why MINOR.** This release adds a comment directive, a marker, a package, a subcommand, and a
generated companion file; nothing it changes is breaking under the rules above. A graph declaring
no `//servo:config` emits a byte-identical file.

### Added
- **`//servo:config`: generated configuration.** Mark a struct with
  `//servo:config prefix=POSTGRES`, tag its fields
  (`config:"max_conns,default=10"` — the grammar is four words: name, `required`,
  `default=`, `secret`), take it as a constructor parameter, and `servo generate`
  writes the loader: `servo_config_gen.go` beside the type, plain steppable Go that
  applies defaults, reads the environment (`POSTGRES_MAX_CONNS`), and reports every
  missing required variable in one startup error that names each one. Tags and
  defaults are validated at generate time — a misspelled option or a `default=` that
  doesn't parse as the field's type fails `servo generate`, not a deploy.

  The loader lives in the config's own package, which is what a reflection-based env
  library can never offer: the struct and every field stay unexported, secrets
  included, and error paths for a field tagged `secret` never echo the value they
  rejected. The one export the package gains is the loader itself (`ServoConfig`),
  because the injector lives in another package and Go has no narrower door; it
  returns the unexported type, which the generated `New` receives by inference,
  holds as a local — deliberately never an `App` field — and passes to the
  constructor. Hand-written constructors for a config type, colliding
  environment-variable claims between two used configs, and a scoped constructor
  trying to borrow one (configs are locals; scopes read borrowed singletons off the
  `App`) are all diagnostics.

- **`servo.ConfigFile("config.yaml")`: one file, three formats, one tag.** Declared
  in the spec, per injector; precedence per setting is default, then the file's
  section (`postgres.max_conns`), then the environment, which always wins. The
  path's extension — `.json`, `.yaml`/`.yml`, or `.toml`, checked at parse time —
  decides which decoder the generated code carries, so an env-only or JSON app stays
  stdlib-only and a yaml app gains `gopkg.in/yaml.v3` in *its own* module; servo's
  module depends on no decoder. One `config:` tag drives every source — there are no
  `json:`/`yaml:`/`toml:` tags to keep in sync, because the map-walking code is
  generated. At runtime `CONFIG_FILE` overrides the path (same extension family
  only); the declared path may be absent, since every setting can still arrive from
  the environment, but an explicitly set one must exist. Injectors sharing a config
  must all declare a file or none — the companion loader is one file with one
  signature — and `servo generate` refuses the mix before writing anything.

- **`conf` package.** The stdlib-only coercions behind generated file reading:
  the three decoders disagree about numbers (JSON says `float64`, yaml.v3 says
  `int`, TOML says `int64`), and that normalization is written and tested once
  rather than stamped into every loader. Its errors name types, never values.

- **`servo config`: the operator's manual, from the generator.** Prints every
  setting one injector's graph reads — environment variable, file key, type,
  required/default/secret, declaring field — as a table or `--json`. Only a
  build-time resolver can print this without running the binary.

- **Config nodes in every view.** Used configs appear at level 0 in the generated
  file's header, `App.Graph()`, `servo graph`, `servo explain` (provider:
  `pkg.ServoConfig (generated, env prefix …)`) and `servo why`; `servo check`
  diffs companion loaders exactly as it diffs `servo_gen.go`.

- **`servo-vet` checks directives.** A typo'd comment directive is otherwise just a
  comment — it compiles, generates, and silently loads nothing — so the analyzer
  flags an unrecognized `//servo:` name, malformed `//servo:config` options, a
  directive on a non-struct, and one placed where the generator never looks.
  `servo.ConfigFile` joins the markers flagged outside `servoinject`-tagged files.

### Changed
- **The tutorial runs on the directive.** `examples/tutorial` dropped its
  hand-rolled `internal/config` package (and with it the `caarlos0/env`
  dependency): every package's config now carries `//servo:config`, the notifier
  borrows `natsbroker.Config` instead of re-declaring `NATS_URL` (two claims on one
  variable is now a refused collision), and the session scope demonstrates the
  documented singleton-carrier workaround (`session.Settings`) for the rule that a
  scoped constructor cannot take a config directly. The formerly unprefixed
  variables gained owners: `ADMIN_ADDR` → `HTTP_ADMIN_ADDR`, `LOG_LEVEL` →
  `OBS_LOG_LEVEL`, `OTLP_ENDPOINT` → `OBS_OTLP_ENDPOINT`. The `NewTestApp` tests
  stopped setting `POSTGRES_DSN`/`REDIS_ADDR` — the overrides removed those
  configs' consumers, so the requirement disappeared with them, which is the model
  working. docs/tutorial chapter 3 teaches the directive (the design lesson it
  taught — each package owns its settings — is unchanged; the mechanism is now
  generated), and every later chapter's listings and generated-output excerpts
  were refreshed against the real regenerated files.

  While migrating, the scoped-config rule itself was refined: a config resolved
  *through* a borrowed singleton from inside a scope's sub-graph is legal (the
  singleton is built by New, where the loaded value is in scope) — only a scoped
  member taking the config type directly is refused. The check moved from
  traversal time to `checkConfigs`, where membership is settled.

## [3.3.0] - 2026-08-30

**Why MINOR, and not PATCH or a new major.** This release adds two markers, a package, two
subcommands and a test helper, so it is more than a patch; nothing it changes is breaking under the
rules above, so it is not a major. Three changes come closest, and none of them lands — they touch
behaviour those rules call breaking *if it was documented*, and none of it was. `servo.RunStop` now
recovers a panic in a stop phase: its doc comment never said what happened to one, and what happened
was that the process died from a goroutine no caller could reach. `servotest.NoLeaks` keeps its
signature and its behaviour exactly; only its doc changed, because the doc described a check goleak
does not perform. And `servo -h`, which used to exit 1 printing `generate`'s flags, now prints usage
and exits 0 — `-h` is not something a `servo check` CI step can depend on. A graph that declares
neither `servo.Value` nor `servo.Include` emits a file that differs only by the rollback fixes
below.

### Added
- **`servo.Value[T]()`: a value the caller supplies, instead of a global read back
  through a provider.** Everything else in the graph is resolved by finding the one
  function that produces it. A value that only exists once the process is running —
  a parsed flag set, a version string injected at link time, a `*sql.DB` opened by a
  test harness — has no such function, and the workaround servo left open was a
  package-level `var` in `main` read back by a small unexported provider beside it.
  That is the global-lookup pattern v3 exists to remove, reintroduced by the one gap
  in the model.

  A spec declaring one gets `type Values struct{ ... }` and
  `func NewWith(ctx context.Context, v Values) (*App, error)` alongside an unchanged
  `New(ctx)`, which delegates with the zero value and says so in its own doc comment.
  A `servo.Value` beats any provider that also produces the type — declaring one is
  how you say "this comes from the caller", which is only meaningful if it wins — and
  a declared value nothing depends on is a diagnostic rather than a struct field
  every caller keeps supplying and the app never reads. Supplied values appear in
  `App.Graph()`, `servo graph`, `servo explain` and `servo why` at level 0, bound
  as `supplied`.

  Internally it is a fourth `NodeKind` that never enters `Resolved.Order`, the same
  trick the two scope kinds use — which is why a graph declaring no value emits a
  byte-identical file.

  `docs/limitations.md` had this filed under *consequences of resolving at build
  time*, a category it introduces as permanent. That was never sound: wire resolves
  at build time, emits plain Go, uses no reflection, and has injector parameters. The
  entry now says what is actually still impossible — a per-call or per-test value
  inside the graph, since a supplied value is one per app.

- **`servo.Include(fn)`: one marker set, several injectors.** A module with three
  binaries had three specs, and everything below the transport was written out three
  times. `examples/tutorial` was the proof: `diff cmd/orders/spec.go
  cmd/ordersgin/spec.go` was two lines. Each spec made eleven marker calls, and ten
  of them — four `Bind`, four `Override`, a `Scoped` with its `Linger`/`Max` policy,
  and a shared `Root` — were identical in all three, copy-pasted with their comments.
  Adding a fifth `Bind` or changing `Max` was a three-file edit nothing checked, which
  is the same multi-place edit `comparison.md` uses to argue against hand-written
  wiring.

  `servo.Include` names a `func() []servo.Marker` and splices its list in where the
  call sits. The function is never run: its body is read as syntax exactly as
  `Build`'s own argument list is, which is why the shape is narrow and checked —
  one `return`, one slice literal, marker calls. It may live in another package, and
  that file needs the `servoinject` constraint for the same reason a spec file does;
  both halves are refused with their own message. Includes may nest, and a cycle
  reports the path that closed it. A `Bind` or `Override` written in the spec file
  supersedes an included one for the same interface, which is the only ordering that
  makes a shared set worth having; two local ones for the same interface remain the
  ambiguity they always were.

  `examples/tutorial` now carries `internal/wiring/wiring.go`, and its three specs
  are nineteen lines each instead of fifty-three — each making two marker calls, the
  `Include` and its own transport's `Root`.

- **`servo version`, and `servo help`.** Ten documented subcommands and no path to
  them from the tool: a bare `servo` generates (the documented default), `servo help`
  was an unknown command, an unknown command printed no alternatives, and `servo -h`
  printed `generate`'s four flags. `help`, `-h` and `--help` now print the command
  list and the shared build flags, and an unknown command prints the list too.

  `servo version` matters more than it does for most tools, because servo writes
  files you commit and gates them with `servo check`: two machines on two versions
  produce a diff in a file neither of them edited, which reads exactly like a
  forgotten regenerate. `check`'s stale report now names the running version and the
  `go get -tool` line that pins one for everybody.

- **`github.com/okian/servo/v3/servovet`.** `var Analyzer` lived in
  `cmd/servo-vet`, so nothing could import it — while five places in the docs promised
  the two checks reach your editor, and `cli.md` named golangci-lint's plugin system
  as the mechanism. That system imports the analyzer package and registers the
  analyzer, which a `var` in `package main` cannot support. The analyzer moved to its
  own package; `cmd/servo-vet` is now the singlechecker binary wrapping it. It also
  flags untagged `servo.Value` and `servo.Include` calls, `Include` being the one most
  likely to be written outside a spec file.

- **`servotest.NoNewLeaks`.** `NoLeaks` calls `goleak.VerifyNone` with no options, and
  goleak has no notion of when a goroutine appeared — so a goroutine left behind by an
  earlier test is reported against whichever test calls `NoLeaks` next. Its godoc and
  the reference both claimed it checked "any goroutine started during the test". The
  combination that triggers it is one servotest advertises: `Timeout` exists to
  exercise the abandoned-node path, and an abandoned node is by definition one that
  never returns. `examples/basic` passes today only because the deliberately-leaking
  test is last in its file and the one test after it is the only one in the package
  without a `NoLeaks` call.

  `NoLeaks` keeps its signature and its behaviour, and its doc now says what it
  actually checks. `NoNewLeaks` takes the baseline first and returns the check —
  `defer servotest.NoNewLeaks(t)()` — which is the call shape that makes a baseline
  possible at all.

- A CI job that builds the documentation site. It is five hand-written Jekyll
  layouts, a Liquid-templated search index and a hand-formatted stylesheet published
  straight to Pages from `docs/`, and nothing in front of a pull request looked at
  any of it.

- **Build flags, and one generated file per build configuration.** The seven commands that load
  packages (`generate`, `check`, `graph`, `explain`, `why`, `list`, `doctor`) now accept `--tags`,
  `--mod`, `--modfile` and `--overlay`, with the same names, syntax and meaning as `go build`.
  `--tags` changes the graph servo resolves, so providers behind `//go:build prod` participate in
  it.

  The generated file's build constraint is now the spec file's own constraint with `servoinject`
  negated — not a fixed `!servoinject` — conjoined with the tags the graph was resolved under. A
  spec gated `//go:build servoinject && prod` generated with `--tags=prod` writes
  `servo.prod_gen.go` gated `//go:build !servoinject && prod`, which coexists with the default
  variant instead of overwriting it. Mutual exclusion between variants comes from constraints you
  write in your own spec files; servo never invents a negation, so it never needs to know the full
  variant set. See [Build variants](docs/reference/cli.md#build-variants).

  A generation with no build flags is byte-identical to before: same `servo_gen.go` name, same
  `//go:build !servoinject`, no committed file moves.

  Servo deliberately does *not* take its tags from `GOFLAGS`, which is the one place this diverges
  from the go command: `go build` makes a binary you discard, `servo generate` makes a file you
  commit, so an inherited tag must not change what lands in the diff. Every other `GOFLAGS` entry
  reaches the go command untouched.

  Generating two variants whose constraints are not mutually exclusive is refused, by `generate`
  and by `check`, naming both files — servo derives constraints from your spec files and never
  invents a negation, so it detects the overlap rather than silently resolving it.

  `servo doctor` inventories the generated files beside each spec: it reports one produced by a spec
  that no longer exists (which nothing else notices — the orphan keeps compiling into whichever
  build satisfies its constraint and is never regenerated), and lists the variants the current
  flags did not check, with the command that would. `servo init --tags=prod` scaffolds a variant
  spec already gated, and names any sibling spec that does not yet exclude the new tags.
  `examples/variants` is a working two-variant project, built, tested and checked both ways in CI.

  `servo-vet` now refuses `-tags` rather than accepting it silently: it is go/analysis's own
  documented no-op, so the run would have analysed only the default configuration. The error names
  the invocation that works, `go vet -tags=... -vettool=$(which servo-vet) ./...`.

  Tags that cannot distinguish one build from another are rejected up front rather than failing
  somewhere unhelpful: `GOOS`/`GOARCH` names (which break the standard library when passed as
  tags), the toolchain's own `unix`/`cgo`/`race`/`go1.N` family, tags containing characters no
  `//go:build` line could name, `ignore` (which compiles every deliberately-excluded file in the
  module and the standard library), and uppercase tags (whose variant file names would collide on a
  case-insensitive filesystem).

### Changed
- **Every rollback runs on a context the signal that triggered it cannot cancel.**
  Both paths inside the generated `New` passed `New`'s own `ctx` down —
  `_ = a.stopX(ctx)` for a construction failure, `a.Shutdown(ctx)` for an Init
  failure — and every `main` in the docs and the examples hands `New` the
  `signal.NotifyContext` context. So a SIGTERM arriving mid-startup, which is a
  rolling deploy or a pre-empted crash-loop restart, cancelled it, aborted an Init,
  and then the unwind was handed a context that was already done. `servo.RunStop`
  derives its budget from it, so `Done` was closed before the `select` ran and every
  node was reported abandoned without its `Drain`, `Flush` or `Stop` getting a
  chance to do anything — the real startup error buried under a wall of
  `abandoned: context canceled`.

  The rollback now uses `context.WithoutCancel(ctx)`. This is the rule the project
  already stated in two other places: `lifecycle.md` says handing a cancelled context
  to shutdown would abandon every node instantly, and scoped teardown already
  stripped the cancellation for exactly that reason. `RunStop` still caps each phase,
  so nothing here can hang.

- **An Init failure whose rollback came out clean returns the bare error.** `Report`
  satisfies `error` by value, so it is never nil and `errors.Join` never skips it —
  and a clean report's `Error()` is the empty string, which `errors.Join` still
  separates with a newline. Every ordinary startup failure therefore returned
  `"connection refused\n"`. `errors.Is` and `errors.As` were unaffected; what broke
  was every log field and every `%w` wrapping built from it.

- **`servo.RunStop` recovers a panic in a stop phase.** It ran every `Drain`, `Flush`,
  `Stop` and cleanup in a goroutine with no recover — servo's goroutine, not the
  caller's, so no `recover` in `main` could reach it. One panicking `Stop` killed the
  process mid-unwind, leaving every node behind it running and no `Report` to say
  which. It is now reported as one failed node, with the panic value and the stack,
  and the rest of the teardown continues.

- **`servo init` scaffolds a workflow that runs.** The `//go:generate` directive was
  written into the spec file, which carries `//go:build servoinject` — and `go
  generate` honours build constraints, so `go generate ./...` exited 0, printed
  nothing, and generated nothing. Adding the tag then hit the second half: a consumer
  requires servo for the marker package alone, so the generator's own dependencies are
  not in their build list and `go run github.com/okian/servo/v3/cmd/servo` fails on a
  missing `go.sum` entry. Nothing in the repository ran that path — every workflow
  step invokes `go run ./cmd/servo` from the root module, where the dependencies are
  present.

  `init` now writes an untagged `servo_generate.go` holding only the directive, as
  `//go:generate go tool servo generate`, and prints the one-time `go get -tool
  github.com/okian/servo/v3/cmd/servo` that makes it work and pins the version.
  `githooks/pre-commit` uses the same form.

- **`servo graph --format=json` and the generated `App.Graph()` now serialise
  identically**, which is what both their doc comments already claimed. They differed
  twice: the CLI wrote a non-nil empty `deps` (`[]`) beside a nil `capabilities`
  (`null`) while the generated literal wrote `nil` for both, so the CLI output was not
  even self-consistent and a consumer iterating `node.deps` worked against one
  producer and failed against the other; and the CLI emitted absolute machine-local
  paths where emission deliberately relativises against the module root.

- **Every `main` bounds its own shutdown.** All ten commands called
  `app.Shutdown(context.Background())`. The fresh context is right — the context above it is
  already cancelled, and that cancellation is what started the shutdown — but a bare
  `context.Background()` puts no ceiling on the unwind. `servo.RunStop` caps each node at
  `servo.DefaultStopBudget`, so nothing hangs, but nothing caps their sum either, and a container
  runtime sends SIGKILL when its grace period expires regardless. Each `main` now derives a
  `shutdownTimeout` context and hands that to `Shutdown` — and, in the tutorial's three commands,
  to the admin listener as well, so both are stopped inside one budget. The README, the lifecycle
  reference and the landing page show the same shape.

  Tests keep `context.Background()` deliberately: `go test -timeout` already bounds them, and
  `t.Context()` is cancelled during cleanup, which is exactly the cancellation a shutdown context
  has to survive.
- **The landing comparison is laid out as one premise and two branches.** The shared project sits
  above a full-width caption rule with its code at its natural measure rather than stretched into a
  panel of empty background; the two `main` files sit below it, separated by a vertical rule, with
  tops aligned rather than stretched to equal height — they are not the same length, which is the
  finding. Each caption carries that finding as data: 58 lines against 22.
- **The tutorial is laid out as ports and adapters, under `internal/`.** Every package it builds
  moved from the module root into `examples/tutorial/internal/`, and each adapter now sits under
  the port it implements: `broker/natsbroker/` and `broker/notifier/`, `cache/redis/`,
  `repository/postgres/` and `repository/migrations/`, `transport/api/`, `transport/ginapi/`,
  `transport/grpcapi/`, `transport/admin/` and `transport/openapi/`. A reader looking for "what
  speaks HTTP" finds one directory rather than five siblings sorted alphabetically among the rest.
  Chapter 2 previously said the flat layout was the deliberate choice; it isn't, and no longer
  says so. This is a change to the example module only — servo resolves providers under
  `internal/` exactly as it does anywhere else, and no servo API changed.
- **The tutorial now implements all seven capability interfaces, not five.** `Notifier` gained
  `Drain` (unsubscribe, then wait for handlers already running) alongside `Stop` (close the
  connection), which is the one place the difference between the two is visible: draining an
  at-least-once consumer has to let in-flight messages finish acking before the connection goes,
  or they are redelivered. The three servers gained `Ready`, which reports whether the listener is
  actually bound rather than whether the constructor returned. `Flush` on `Session` and the rest
  were already there.
- **The reference says what each capability interface is *for*.** `docs/reference/lifecycle.md`
  and `docs/reference/servo-package.md` previously gave the seven signatures and little else. They
  now say when each method is called, in what order, against which deadline, and what servo does
  with a returned error — including why the optional cleanup func returned by a constructor is not
  redundant with `Finalizer`.
- **The landing page compares the two `main` functions, not the two constructors.** The
  constructors, their `Config` types and their `config.Parse` calls are identical whether or not
  you use servo — showing them as "what servo generates" implied otherwise. The section is now
  three blocks: the project once, then its `main` written by hand and the same `main` with servo,
  side by side. Both blocks are only `main`, and neither shows generated code — the generated file
  is not what the comparison is about, the hand-written `main` it replaces is.
- **The Gin and gRPC transports are tutorial chapters again, not reference pages.** They walk
  through rebuilding one chapter's code in another framework, against the tutorial's own
  module — a tutorial's job, not a lookup surface's. `docs/reference/transport-gin.md` and
  `docs/reference/transport-grpc.md` are now `docs/tutorial/11-gin-transport.md` and
  `docs/tutorial/12-grpc-transport.md`, sitting directly after the API layer they re-implement.
  Chapters 11 through 19 shift to 13 through 21; the tutorial is 21 chapters. Both new chapters
  are optional — chapter 13 follows on from chapter 10 whether or not you read them. The old
  `/reference/transport-gin.html` and `/reference/transport-grpc.html` URLs redirect to them,
  so an existing link still lands on the page it meant.

### Fixed
- **Four more identifier collisions that made `servo generate` exit 0 and the output
  fail to compile** — the same family as the `Result`, `StopFoo`, `StartupReport` and
  `A`/`Ctx`/`Err` fixes below. Found by enumerating the derived-name templates in the
  emitters and diffing that set against what the allocator actually reserves, then
  compiling a real module for each; all four now have a fixture in
  `cmd/servo/generate_check_test.go`, which builds and compiles, because a compile
  error is the only honest witness.
  - A provider returning `(T, func())` derives a `<field>Cleanup` field from its
    node's variable name, and only the variable was reserved. A component named `Foo`
    with a cleanup, beside one named `FooCleanup`, declared the field twice.
  - New's locals share a scope with the package identifiers its own body qualifies
    calls with, and the allocator did not know those identifiers were spoken for. A
    component named `Servo` took the local `servo`, and every `servo.StartupNode` the
    Init phase writes after it resolved against that local. The same shape applied to
    `errors`, `time`, `sync`, `fmt`, `atomic`, `errgroup`, `context`, `os`, `signal`
    and `syscall`.
  - The import manager could not alias a single-segment path, so a user package named
    `errors` reached first while qualifying a type took the identifier, and the stdlib
    import that arrived second was rendered under the same name beside it.
  - The shadow guard compared later nodes' *result type* packages, but the call it
    protects is qualified by the provider *function's* package. The two coincide only
    for the idiomatic `package foo; func New() *foo.Foo`, which is exactly what the
    existing regression test covers — so every factory or facade package, and the
    ordinary `func New(cfg Config) (*sql.DB, error)` shape, went unguarded.

- **A constructor returning a nil cleanup func crashed the process during `Shutdown`.**
  `(T, func())` is a documented provider shape and returning `nil` when there is
  nothing to clean up is ordinary Go, but the emitted wrapper called it
  unconditionally — inside `RunStop`'s own goroutine, where no `recover` in `main`
  could reach the panic. Both the App-level and the scope-entry teardown now skip a
  nil cleanup; the merged `NodeResult` is identical either way.

- **The variant-overlap refusal printed a paste-in fix that was not a build tag.** Its
  `%[1]s` resolved to the generated *file* name where it meant `servoinject`, so it
  told the reader to write `//go:build servo.prod_gen.go && !prod` — which would
  silently exclude the spec file from every build. Because an explicit argument index
  also suppresses `fmt`'s `%!(EXTRA ...)` marker, nothing showed that the build tag
  it was handed went unused.

- **README undercounted `examples/diagnostics`.** It said seven fixtures; there are
  eight, and that example's own README says so. The missing one is `noscopekey/`.
  The workflow comment beside the build step said seven too. All eight have had a
  test in `cmd/servo` the whole time.

- **README's Layout block omitted three example modules that exist** —
  `examples/diagnostics/`, `examples/variants/` and `examples/migrate/` — while
  `ARCHITECTURE.md` points at that block as the authoritative file list, and README
  links to two of the three from elsewhere in the same file.

- **`examples/migrate/README.md` quoted line numbers four off from reality.** The
  fixture gained a comment block and the transcript was not re-pasted. It is the one
  example with no generated file, so `servo check` has nothing to compare there and
  no workflow step runs `servo migrate` at all; a test now holds the README against
  the real output.

- **`docs/reference/cli.md` described `servo-vet -tags` as a silent no-op.** It is
  refused, with exit code 2 — a change the [Unreleased] *Added* section above records,
  landing in the same commit as the paragraph that contradicts it.

- **`cli.md`'s `doctor` section was missing a line prefix and two checks.** It said
  every line is `[OK  ]`, `[FAIL]` or `[WARN]`; `doctor` also emits `[INFO]` for the
  variant inventory, printed outside the report closure so it never affects the exit
  code — which matters to anyone parsing the output in CI. The orphaned-generated-file
  `FAIL` and the inventory itself were absent from the table.

- **`diagnostics.md` and `spec.md` quoted a "missing runtime package" message servo no
  longer prints.** The real one names the build configuration and calls out the
  build-tag case, which is the half a reader searching for their actual error string
  would not have found.

- **The lifecycle contract that `Drain`, `Flush` and `Stop` may run on a component
  that was constructed but never `Init`ed** is now written down. Construction rollback
  stops every earlier node, none of which has reached Init; Init-failure rollback
  calls `Shutdown` over the whole graph, including levels above the failure. So the
  most obvious `Finalizer` there is — `return d.pool.Close()` — nil-dereferences on
  the rollback path, and nothing in `lifecycle.md`, `generated-api.md` or the
  capability doc comments said so.

- **The landing page's diagnostic was a mock-up, and overcounted.** It claimed three types
  implement `store.Store` and listed `examples/basic/memory` as one of them. Deleting the
  `servo.Bind` line from `examples/basic` and running `servo generate` reports **two** —
  `mockstore.Store` and `postgres.DB`. Nothing imports `memory`, so it is not in the package set
  servo loads and can never be a candidate, however well it satisfies the interface. The block is
  now that real output, and a comment beside it records the command that reproduces it.
  `memory`'s own package doc claimed the opposite and now says what actually happens, which is a
  resolution rule worth knowing: reachability from the injector decides candidacy, not
  implementing the interface.
- **The landing page showed `servo.Build(...)` at file scope**, which is not valid Go — the real
  spec files wrap it in a function. The block is `main` alone now, and where the roots are
  declared is said in prose instead.
- **A build constraint below the package clause was treated as a build constraint.** `go/build`
  only reads the file header, so servo could call a spec file "correctly gated" while it compiled
  into the real binary. Servo now resolves constraints the way `go/build.shouldBuild` does: header
  only, a `//go:build` line wins outright, and otherwise every `// +build` line is ANDed rather
  than only the first. `servo-vet` and the generator share the one implementation, so they cannot
  disagree.
- **Tutorial code blocks that had drifted from `examples/tutorial`.** Chapter 10's `api.New` took
  no logger while both middlewares require one, so the printed
  `recoverMiddleware(loggingMiddleware(mux))` passed one argument to a two-argument function and
  could not compile; its `api/middleware.go` block imported `log/slog`, which nothing in it used,
  instead of the `observability` package it names. Chapters 15 and 16 reprinted `api.New` without
  `sessions` (added in chapter 14) or `log`, and chapter 16's router dropped the `GET /me/recent`
  route chapter 14 has the reader build. Chapter 16's constructor now matches `api/server.go`
  parameter for parameter and route for route.
- **Tutorial figures that no longer matched the module.** Chapter 17 claimed 43 tests across 14
  files while its own diagram summed to 52 across 15; the real count is 64 across 19 files that
  declare tests, retiered as 33/9, 17/3, 3/3 and 11/4. Chapter 1's endpoint table was missing
  `GET /me/recent` and the two contract routes. Chapter 2's Makefile lacked the `run-gin` and
  `run-grpc` targets that chapters 11, 12 and 19 tell the reader to run. Chapter 10 said the
  OpenAPI handler is mounted on the public listener without ever showing the two lines that
  mount it.

## [3.2.0] - 2026-08-29

### Added
- **Gin and gRPC as transport choices in `examples/tutorial`.** The same service layer now sits
  behind three transports, each its own injector: `api`/`cmd/orders` (`net/http`),
  `ginapi`/`cmd/ordersgin` (Gin), and `grpcapi`/`cmd/ordersgrpc`, which serves gRPC and REST on a
  single port. New chapter 19 covers what differs between them and what deliberately does not; the
  old chapter 19 is now 20.
- `examples/tutorial` gains packages `admin` (health, readiness and metrics on their own listener,
  never the public one, asserted by a test in every variant) and `openapi` (the contract, embedded
  and served with a Swagger UI).

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
