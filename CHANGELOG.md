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

**Supported Go versions.** servo supports the two most recent Go releases, matching Go's own
support window, and go.mod's `go` line is the older of the two. When Go ships a release, the next
servo release raises the line to the release before it. That is a minor bump, as the move to 1.27
in 3.2.0 was: it changes nothing this section defines as breaking.

## [3.5.0] - 2026-10-27

### Added
- **`servo.Main(New)`: the documented main.** Every example and every page used to show about 30
  copied lines of signal handling, logging and shutdown, and the copies shared three faults: a
  failed `Run` or an unclean shutdown exited 0, so systemd and Docker on-failure restarts never
  fired and a Kubernetes Job reported success; a second signal was swallowed until `Shutdown`
  started; and with the standard logger routed through a slog handler at warn level, a failed
  start printed nothing at all. `servo.Main` handles SIGINT, SIGTERM and SIGHUP (graceful, since
  Go's default for SIGHUP kills the process mid-request), exits with status 128+signal on a second
  signal from the moment the first arrives, logs failures at ERROR — writing to stderr when the
  logger would drop an ERROR line — shuts down under its own deadline (25 s by default, below
  Kubernetes' 30 s grace period; `servo.ShutdownTimeout` changes it), and exits with
  `servo.ExitCode`. `servo.Logger` supplies the logger.
- **`servo.ExitCode(runErr error, r servo.Report) int`** for mains that need their own shape: 0 when
  `Run` returned nil or only the shutdown signal's cancellation and the report is clean, 1
  otherwise.

- **`servo.NodeError`: startup and `Run` failures name their node.** An `Init` failure used to come
  back exactly as the component produced it — `dial tcp 127.0.0.1:5432: connect: connection
  refused`, with three components that dial — so the fatal line of a failed start rarely said what
  had failed. Every `Init` and `Run` failure is now a `*servo.NodeError{Node, Phase, Err}` rendering
  as `*postgres.DB: Init: dial tcp …`, with `Unwrap` returning the component's own error, so
  `errors.Is` and `errors.As` on that error keep working. A constructor's error is still returned
  undiluted, as the lifecycle reference promises.
- **`Report.Err()`**, `nil` exactly when the report is clean. `Report` satisfies `error` by value, so
  a `Report` stored in an error variable is never nil, and `if err != nil` on it holds even for a
  perfect shutdown; `app.Shutdown(ctx).Err()` is the form that answers that comparison honestly.
- **`servo.DefaultInitBudget`** bounds each `Init` call, scoped instances' included. Zero, the
  default, keeps today's behaviour — `New` waits for every `Init`. Set above zero, it is the deadline
  of each `Init`'s context and the limit on waiting for it: an `Init` that respects its context fails
  at the deadline with an error naming the budget, and one that ignores it is abandoned at the same
  moment and left running while `New` rolls back, the way `RunStop` treats an overrunning `Stop`. A
  dependency that accepted a connection and never answered used to hold `New` open with no output
  and no error.
- **`servo.DefaultRunGrace`** (5 seconds): how long `Run` keeps waiting for its runners once its
  context is done. See the fix below.
- **Reports encode to JSON and text.** `NodeStatus` marshals as `"ok"`, `"failed"` or `"abandoned"`
  (and unmarshals from them, and from the bare numbers earlier releases wrote, so stored reports keep
  decoding); `NodeResult` encodes as `{"name", "status", "err"}`, the error as its message and
  omitted when nil; `Report` as `{"nodes": [...]}`. The default encoding wrote `"Status":1,"Err":{}`,
  so a report served from a health endpoint said a node failed and never why.
- **`github.com/okian/servo/v3/servoimpl`, the runtime only generated code calls.** Generated code
  lives in the user's package, so whatever it calls has to be exported, and exporting it from
  `servo` froze it as public API. `servoimpl` holds those helpers — the `Init` phase and `Run`
  supervisor, the stop helpers, the scope linger hook, the second-signal exit — and is exempt from
  the compatibility promise, changing with the generator that calls it, except for its `ContractN`
  constants. It imports `servo` and the standard library; `servo` never imports it. Not for direct
  use; documented in the `servo` package reference.

- **`ShutdownDelay` on `servo.HTTPConfig` and `servo.HTTPListener`: rollout-safe shutdown.** The
  generated stop path used to drop readiness and call `srv.Shutdown` in the same instant, so a load
  balancer or Kubernetes endpoint still routing to the pod turned its last requests into
  connection errors. A server now reports not-ready, keeps serving for `ShutdownDelay` (bounded by
  the shutdown deadline), then drains. Zero keeps the old behaviour. With several groups the delays
  overlap rather than add up.
- **`servo.StatusFor(code int) servo.HTTPStatus`** builds a status outside the `Status` table — a
  499, or an upstream service's code passed through. `servo.Status` is documented as a table to
  read; assigning to it was never meant to work, and making it immutable would change its type.
- **`MaxHeaderBytes` on `HTTPConfig` and `HTTPListener`**, passed to the generated server.
- **`servo.Use` accepts plain middleware functions.** A node whose type is
  `func(http.Handler) http.Handler`, or a defined type with that underlying type, is emitted as a
  call. Every otelhttp, chi or gorilla middleware used to need a wrapper type with a `Middleware`
  method, because resolution refused anything else.

- **`middleware.ClientIPKey(trusted ...netip.Prefix)`: rate limiting behind a proxy.** Behind a
  load balancer, an ingress or a CDN, `RateLimit`'s default key — the connection's peer address —
  is the proxy's, so every client shared one bucket. `ClientIPKey` returns a
  `RateLimitConfig.KeyFunc` that starts at the peer and, while the address it holds is inside one
  of the trusted prefixes, walks `X-Forwarded-For` from the right; the first address no prefix
  covers is the client. Entries left of it are whatever the client sent and are never read, so the
  header cannot be used to choose a key, and an entry that is not an address ends the walk at the
  trusted hop that wrote it. With no prefixes it is the default key, and the reference now says
  plainly what that default means behind a proxy.
- **`RateLimitConfig.MaxKeys` bounds the bucket table.** Zero means 100,000 keys (about 12 MiB);
  a negative value is a constructor error. At the cap a new key evicts the least recently seen
  one, which starts over with a full bucket if it returns, so a flood of distinct keys makes the
  limiter forget clients rather than grow without bound or turn them away. Before, a million keys
  held about 100 MiB and nothing ever capped it.

- **A constructor's `context.Context` is the context `New` was called with.** A parameter of that
  type now resolves to the ctx handed to `New`, `NewWith` or `NewTestApp` — supplied, like a value,
  never searched for — and the generated call passes `New`'s own `ctx`. It used to be an ordinary
  exact-type lookup, which found ten functions in the standard library returning `context.Context`
  and told the reader to remove or rename all but one; the documented workaround,
  `servo.Value[context.Context]()`, made a plain `New(ctx)` pass a nil context. A declared
  `servo.Value[context.Context]()` still wins, and so does a `Bind` or `Override` naming the type.
  It is a construction-time context, for a dial or a ping, not for work that outlives the call. Only
  constructors `New` calls can take it: a scoped constructor runs inside `Acquire`, after `New`
  returned, and is refused with a pointer to `Init(ctx)`, as is a second `context.Context` among a
  `ScopeKey` extractor's or a `//servo:` handler's parameters. The node appears in `explain` and
  `App.Graph()` deps as `context.Context`; no field is added to `App` or `Values`.
- **"No provider" names the near misses.** The diagnostic now ends with what came close, one line
  each with its position: `rejected:` for a main-module function the scan refused that would have
  provided the type (variadic, generic, unexported, the wrong result shape — an instantiation of a
  generic constructor counts), `unusable:` for a candidate the injector cannot refer to, `near
  miss:` for a provider of the type's `T`/`*T` twin or a value whose pointer-receiver methods are
  what an interface needed (`Get has a pointer receiver, so only *memory.Store does`), and `note:`
  when the type is unnamed or primitive and so can never be provided. A package-level variable of
  function type is named too. Before, the reason sat only in `servo list --rejected`, which no
  message mentioned, and the reference claimed the opposite. The same engine is exported to the CLI
  as `resolve.NearMisses`, for `explain` and `why`.
- **`servo list --rejected` answers the question it is asked.** Each refused function names the
  type it would have provided, the candidates this injector cannot refer to get a section of their
  own, and methods — every `Init`, `Stop` and `Health` in the module, 66 of 79 lines on the tutorial
  — are grouped by receiver after the functions, one line per type. `--json` keeps one object per
  entry and gains `provides` and `kind` (`function`, `method`, `unusable`), a schema **addition**.
- **servo-vet: three more mistakes.** A directive a space turned into prose — `// servo:get
  /orders`, which Go and `generate` both read as a comment — is flagged when the rest of the line is
  a well-formed route or config directive (generated files are skipped). A capability-named method
  with the wrong signature on a type a constructor returns — `Run() error`, `Stop(ctx)` with no
  error, `Init()` without a context — is flagged, since capabilities are detected by signature
  alone and servo never calls it. And a dot-imported marker, bare `Build(Root[*X]())` in an untagged
  file, is flagged like a qualified one, through `TypesInfo.Uses`; before, it passed servo-vet and
  panicked at runtime. `servo generate` still recognizes only the qualified spelling, which the
  limitations page now says.
- **`generate` notes capability near misses.** For every node a constructor in the main module
  builds, a capability-named method servo will never call, and a `Close` or `Shutdown` with no
  `Stop` to reach it (unless the constructor returns a cleanup func), are reported as notes —
  `servo: note: *comp.Pool has a Close method but no Stop(ctx context.Context) error, so servo will
  not release it at shutdown` — and generation goes ahead. Each used to leave the component at
  `capabilities: none` with nothing said.

- **`--scope=module|workspace` on every command that loads packages.** It decides which modules
  are "the main module" when a `go.work` lists several: the modules whose packages interface search
  may auto-bind, whose `//servo:` routes and `//servo:config` types a graph reads, and whose
  directories servo may write into. `module`, the default, is the module `--dir` is in and nothing
  else — what a `GOWORK=off` build, CI's usual one, calls the main module. `workspace` is every
  module the `go.work` lists, loaded whole, which is how servo read a workspace before. The commands
  a report suggests repeat it, and `doctor` warns about a multi-module `go.work`. Reference:
  [`--dir` and injector scope](docs/reference/cli.md#--dir-and-injector-scope).

- **`servotest.NodeName[T]()`: the name servo gives a type's node.** Reports name nodes by type
  with full import paths — `*example.com/app/api.Server`, never the `*api.Server` source code
  spells — so every order assertion written the way the README, the servotest reference and the
  generated-API page showed it failed in a real module on an order that was right.
  `AssertStopOrder(t, rec, servotest.NodeName[*api.Server](), servotest.NodeName[*postgres.DB]())`
  follows renames and moves with no edit. It is computed from `T` at run time and spells generic
  instantiations the way `go/types` does; a test type-checks servotest's own test package and
  holds it to the generator's naming rule across pointers, generics, maps, `any` and `struct{}`.
  `AssertStopOrder`'s matching is unchanged.
- **`servo completion bash|zsh|fish`** prints a shell completion script: commands, each
  command's flags, the values `--format` and `--mod` take, directories and files where a flag names
  one, `new`'s kinds and mock tools. It is generated from the same command table and flag sets
  the parser uses, so a flag a later servo adds completes once the script is regenerated.
- **`servo why --all`** lists every path from a root to the node, then every path from an HTTP
  declaration — every reason it is in the binary — shortest first, stopping at 100. With `--json`
  it prints an array of the arrays `why --json` prints; without `--all` the schema is unchanged.
- **`servo init` writes `main.go`** when the directory is a `package main` and nothing there
  declares a `main` yet — `func main() { servo.Main(New) }` — and prints it with a `go run` line,
  so `go generate ./... && go run ./cmd/app` works in the first minute. An existing `main`
  function, in any file, or an existing `main.go` is never touched.
- **`servo doctor` checks that `go generate` can run the generator.** It reads every
  `//go:generate` line in the module the way `go generate` does — build constraints with the
  `generate` tag, test files, `-command` shorthands, quoting — and, per injector and variant, says
  which line regenerates it. A covering line in a file `go generate` skips, which is what a
  directive inside the `servoinject`-gated spec file is, is a `FAIL`: `go generate ./...` exits 0
  there having run nothing, and doctor called the module healthy. So is an injector no line
  covers in a module that otherwise uses `go generate` for servo, a covering line whose arguments
  servo would refuse, and a `go tool servo` line with no `tool` directive in `go.mod` — `go
  generate` stops at `no such tool "servo"`. A module that never runs servo from `go generate` is
  a `WARN`, since regenerating from make or CI is a legitimate setup.
- **`servo doctor` compares versions, checks config agreement, and names a workspace.** A line
  saying whether this binary is the servo release `go.mod` requires comes first — a `WARN` on a
  mismatch, which a global `@latest` beside a pinned `go tool servo` makes the normal state, and
  an `INFO` when either side is a development build or a replacement. The cross-injector config
  agreement check `generate` and `check` run now runs here too, an injector whose graph does not
  resolve is reported as that rather than as a stale file, and a `go.work` listing more than one
  module draws a `WARN` explaining `--scope`.
- **`servo doctor` makes the checks `check` makes.** It compiles the generated files it finds
  current, once, together — byte-identical to a fresh generation is not the same as buildable —
  reports each config loader left over from a `//servo:config` type its package no longer
  declares, and every command it suggests repeats the `--dir` and build flags it was run with.

- **`servo generate -v`** lists every generated file on stderr, one line each, as written,
  unchanged or removed. Removals are reported with or without it: deleting a file nobody named
  should never be silent.
- **`servo generate` notes an empty graph.** The empty `servo.Build()` that `servo init`
  scaffolds generates and builds cleanly into a program whose `Run` returns the moment it starts;
  a spec with no `servo.Root` and no `servo.HTTP()` now draws one line on stderr saying so. The
  run still succeeds.

- **`NewTestAppWith` takes a mock per call.** `TestValues` gains a field per `servo.Override`,
  typed as the overridden interface. Left nil, the Override is built by its constructor as
  before; set, the test's own instance is used in its place — no constructor call and no lifecycle
  calls, exactly like a supplied value. A gomock controller can now hold the test's real `t`, so an
  unmet expectation is an ordinary test failure instead of a `PanicReporter` panic, and tests that
  each supply their own mock can run in parallel. The Override's concrete field keeps its name and
  type, so `app.storeMock.GetFunc = …` still compiles; consumers read an interface-typed field
  beside it. An Override whose implementation has a lifecycle, or that some consumer takes as the
  concrete type rather than the interface, gets no field and is built as before. `NewTestAppWith`
  and `TestValues` now exist whenever the test graph has such an Override or reads a
  `servo.Value`. `examples/mocking` shows all three mock generators used this way.
- **Every generated file names the runtime contract it was written against**: `const _ =
  servoimpl.Contract1`, under a comment saying the file needs v3.5.0 or later and to regenerate
  with `go tool servo generate` if the line does not compile. A file generated by a newer servo
  than the one `go.mod` pins used to compile against the older runtime and crash in `RunStop`
  during a rollback, or fail inside a helper whose signature had moved; it now fails on that line.
  The constant lives in the new `servoimpl` package, whose `ContractN` constants are the one part
  of it the compatibility promise covers: a generator that needs newer runtime behaviour emits
  `Contract2`, and none is ever removed within a major version. Config companions carry the guard
  too, and so import `servoimpl`.
- **`GraphNode.Provider`** names the function that builds a node — `postgres.New`, or
  `store.ServoConfig` for a `//servo:config` type — in `App.Graph()`, `servo graph`'s JSON (a
  schema **addition**, `omitempty`) and its text output. Empty for a supplied value and a trace
  wrapper, which no function of the program builds.

- **`ValidateConfig() error` per injector** runs every `//servo:config` loader the graph uses
  without constructing anything, and joins their errors — for an init container or a pre-deploy
  CI step that should fail before the service starts.

- **Releases are cut by a workflow that checks everything before the tag exists.**
  `.github/workflows/cut-release.yml`, run from master with the version
  (`gh workflow run cut-release.yml -f version=v3.5.0`), refuses a version that exists or does not
  come after the latest release, one whose major does not match go.mod's `/vN`, one without its
  CHANGELOG.md section, and a commit go.yml has not passed on — waiting for go.yml if it is still
  running — and runs `gorelease` against the previous release, so a version wrong for the change
  to the exported API is refused too. Only then does it create the annotated tag and start the
  release on it. Before, the tag was the first step: v3.4.1 was published four minutes before CI
  finished on its commit, and a missing changelog section was found after the tag existed, with an
  error that advised re-tagging. Pushing a tag by hand still works; the release then waits for
  go.yml on that commit and stops if it failed.
- **Every release archive has a signed build-provenance attestation.** A release job of its own
  attests each archive through `actions/attest-build-provenance`, and the publish job verifies
  every one — `gh attestation verify <archive> --repo okian/servo`, pinned to the release workflow
  and the tag — before anything is published. RELEASING.md gives the same command to users.
- **RELEASING.md**: how a release is cut, re-run, retracted and verified; the rule that a tag is
  never moved, with the `retract` directive that replaces it; what GitHub's immutable releases
  would change; Gatekeeper's quarantine on the ad-hoc-signed darwin binaries; and the recipe that
  rebuilds a release binary bit for bit — checked against v3.4.1's linux/amd64 binaries, which it
  reproduces byte for byte.
- **CI checks that nothing checked before**: `govulncheck` over every module, nightly and on any
  `go.mod`/`go.sum` change (`.github/workflows/vulncheck.yml`); `gofmt` over every committed Go
  file and `go mod tidy -diff` in every module; spelling (`typos`, with the existing allowlist);
  `actionlint` over the workflows; an offline check of every relative link and `#fragment` in
  the Markdown, plus two checks for links the published site cannot follow — a link whose text
  wraps a line, which Pages leaves pointing at the `.md` file, and any page of the built site
  that still links to one; CodeQL's `actions` language next to `go`; and Dependabot version
  updates, grouped and after a week's cooldown, for the actions, every module and the docs gems.
- **A determinism job regenerates every example on Linux, macOS and Windows** — from a moved
  checkout, a moved module cache, vendored dependencies and a `go.work` — and fails on any byte
  that differs from the committed files, so positions, ordering and line endings that depend on
  the machine are caught where they are introduced. `.github/scripts/determinism.sh` runs the
  same scenarios locally.
- **A nightly toolchain canary** builds and tests the root module and runs every example's
  `servo check` on the newest Go release, resolved afresh each night, and on Go's development
  branch (advisory). servo reads the standard library from the toolchain's export data, whose
  format a Go release can change, as 1.27.2 did; the canary finds such a release before users do.

- **Three guides on the documentation site.** [Coming from wire or
  fx](docs/coming-from-wire-and-fx.md) maps every wire and fx concept onto servo's — `Provide`,
  `Invoke`, `Supply`, `Annotate` with `As`, `Replace`, `Decorate`, `Module`, `Private`, names,
  groups, `In` and `Out`, `optional`, `wire.Struct`, `FieldsOf` and `Value`, injector parameters,
  cleanup functions — and for the ones with no equivalent says what to write instead.
  [servo in CI](docs/servo-in-ci.md) is the workflow a consumer can copy, which `go.yml` never was
  (it runs `go run ./cmd/servo`, which works only inside this repository): `go tool servo check`
  with a `merge_group` trigger and `GOWORK=off`, the exit codes of every command and of
  `servo-vet`, upgrading, `go.work` and `--scope`, build variants, cross-compiling `generate` under
  `go tool` (`GOOS=linux "$(go tool -n servo)" generate`; `GOOS=linux go tool servo` builds the tool
  for Linux and fails with `exec format error`), vendoring, offline use, measured costs, the
  `.gitattributes` and CODEOWNERS lines for generated files, and the pre-commit hook. [Running in
  production](docs/running-in-production.md) is a short checklist: `servo.Main` and an exec-form
  `ENTRYPOINT`, `GOMAXPROCS` (sized from the container's CPU limit since Go 1.25, from the limit and
  not the request) and `GOMEMLIMIT` (no Go release reads the memory limit, so it is yours to set),
  probes, the three durations a graceful shutdown has to fit, HTTP server timeouts and the warning a
  listener without `ReadHeaderTimeout` logs, and profiling on a listener of its own. The docs home
  page links all three, and the README, the preface and the comparison page link the ones they
  touch.

### Changed
- **Every example main and every documented main is `servo.Main(New)`**, with a closure where
  `NewWith` is needed. The lifecycle reference keeps a "writing your own main" section for the
  cases that need control, with `ExitCode` and the separate shutdown deadline explained.
- **Liveness checks the process; dependency checks feed readiness.** The guidance used to map
  `/healthz` — dependency pings — to the liveness probe, so a 40-second database failover failed
  liveness on every replica, restarted them all, and `Init`'s fail-fast kept them crash-looping
  until the database returned. The tutorial's admin server now serves `/livez` (the process
  itself), `/readyz` (`App.Ready`) and `/healthz` (`App.Health`, for humans and dashboards), and
  the lifecycle reference explains probes and how to size `terminationGracePeriodSeconds`.
- **One runtime helper runs every `Init` level, and `Init`'s context is for the call.** The
  generated code called a lone `Init` directly, on `New`'s own long-lived context, and gave a level of
  several an errgroup, whose context dies when the level finishes — so a refresher an `Init` started
  on its context outlived `New` while its node was alone at its level, and died right after `New`
  once an unrelated `Initializer` joined it (`examples/basic`'s production and test files already
  disagreed). `servoimpl.Init` now runs every level, scoped ones included: each call gets a context
  of its own, cancelled the moment it returns — and earlier if a sibling fails, if `New`'s context
  ends, or if `DefaultInitBudget` runs out. Long-lived work belongs in `Run`; the lifecycle reference
  says so. The per-node timings `App.Report()` returns are kept, in completion order. The phase is one
  call with one rollback site, where each level used to carry its own copy.
- **Generated code no longer imports `golang.org/x/sync/errgroup`.** It appeared with the first level
  of two Initializers or the second runner, and a module that required only servo got `servo
  generate` exiting 0 and then `missing go.sum entry … errgroup` from `go build`. The `Init` phase and
  `Run` use stdlib-only helpers in `servoimpl`; after regenerating, `go mod tidy` drops the
  requirement.
- **`Run` reports every failure, never one masked by a cancellation.** The errgroup returned whichever
  error came first, so a runner whose final flush failed after the shutdown signal was hidden behind
  a server that merely returned `context.Canceled` first, and the process exited 0 with the data
  gone. `Run` now returns every failure, each a `*servo.NodeError` (joined when there are several),
  and only when there is none, the first cancellation a runner returned, unchanged — what an ordinary
  shutdown has always returned. A runner returning `nil` still cancels nothing. A lone runner is
  supervised like the rest instead of being called bare.
- **A failed `New` carries its rollback report without joining the error chain.** The unwind's
  report was `errors.Join`ed to the startup failure, so a rollback `Stop` that overran its budget
  made `errors.Is(err, context.DeadlineExceeded)` true for a startup that had failed for an unrelated
  reason — and the only node the message named was that innocent one. The report now rides along
  where `errors.As(err, &report)` finds it and the message still ends with it (`rollback: …`), but
  `errors.Is` does not descend into it. A clean unwind still returns the failure itself.

- **The drain gets the rest of the shutdown deadline, and an overrun cancels the requests.** It
  used to run under a fixed 5-second `DefaultStopBudget` whatever the caller's deadline was, and
  when it didn't finish nothing cancelled the requests still in flight, so handlers kept calling
  dependencies the rest of `Shutdown` then stopped. The drain now takes the caller's remaining
  deadline (or `DefaultStopBudget` when there is none); if it overruns, the server's base context
  is cancelled, so `r.Context()` is done in every handler, and every connection is closed. A
  request cut off that way answers 503, logged at Warn.
- **Error-path logging carries the request context and fixed messages.** Every log line the
  generated adapter writes uses slog's `*Context` variants with `r.Context()`, so a handler that
  reads request or trace ids from the context can attach them, and names the handler and route as
  attributes instead of building the message from them.
- **A client that went away is a 499 at Debug, not an ERROR and a 500.** A handler that correctly
  returns `ctx.Err()` after the client disconnects used to be logged at ERROR as a server failure,
  inflating 5xx rates and paging people for timeouts upstream.
- **`servo.ErrScopeFull` and `servo.ErrScopeClosed` answer 503 with `Retry-After`**, logged at
  Debug — a scope shedding load at its cap or shutting down is not a fault — instead of a 500 and
  an ERROR line per refusal.
- **A missing `ReadHeaderTimeout` is announced at startup.** Zero still means no timeout, as
  `HTTPConfig` documents and `net/http` does, because changing that would be breaking. But a server
  started without a header timeout now logs one warning naming the listener, since a few hundred
  clients that never finish their headers can hold every connection. Every example and the tutorial
  set all four timeouts.
- **The TLS key pair is loaded when the server is built**, so a missing or unreadable certificate
  fails `New` with an error naming the listener and the files, instead of surfacing in `Run` after
  every `Init` — and a certificate replaced on disk is picked up by later handshakes, where it was
  read once and never again.
- **Generated HTTP code calls the runtime through `servoimpl`.** Listener setup, serving, the stop
  sequence and the response contract live in `servoimpl/http.go`, not in every generated file.

- **`middleware.RateLimit` keys an IPv6 client by its /64.** One host is routinely given a whole
  /64, and keyed per address a single client could mint a fresh bucket per request. An
  IPv4-mapped address counts as the IPv4 one. IPv4 keys are unchanged.
- **`middleware.RateLimit` forgets idle buckets a few at a time.** Every 4096th request walked
  the whole table under the limiter's one mutex — 16 to 130 ms at a million keys, during which
  every request waited. Buckets now sit on a recency list, and each request forgets at most two
  idle ones from its cold end, so no request pays for a pass over the table.
- **`middleware.AccessLog` lines carry `route` and `request_id`, and are logged with the
  request's context.** `route` is the pattern the mux matched (`r.Pattern`, such as
  `GET /items/{id}`), present when no middleware between AccessLog and the mux replaced the
  request; `request_id` is `RequestID`'s, read from the context or, when RequestID runs inside
  AccessLog, from the `X-Request-Id` response header it set. `slog.InfoContext` with
  `r.Context()` lets a context-aware handler add a trace id. `middleware.Recover`'s crash line
  gains the same three, logged with `slog.ErrorContext`.
- **`middleware.Gzip` compresses only bodies of 1 KiB or more.** It used to compress everything,
  turning a two-byte body into 27. The status and the first KiB are held until they settle the
  encoding — nothing net/http does not hold anyway — and a response that ends or is flushed
  before then goes out as it is. A handler that set no `Content-Type` now gets the one net/http
  would sniff: net/http sniffs nothing once `Content-Encoding` is set, so compressed responses
  went out untyped, and a sniffed type that is compressed already (an image) is no longer
  compressed again. A 206, or any response with a `Content-Range`, passes through, since
  compressing a range made its `Content-Range` describe bytes the client never received; a
  compressed response drops `Accept-Ranges`; and `Vary: Accept-Encoding` is set on every
  response, including those sent plain to a client that does not take gzip.
- **The reference documents the order the shipped middleware work best in** — `RequestID`,
  `Recover`, `AccessLog`, then the rest, with `Timeout` only on the groups or routes whose
  responses are bounded — and why ([middleware package](docs/reference/middleware.md#order)).

- **Exact-type selection prefers the main module's constructors.** The exact-type lookup covers
  every loaded package, so a module's own `NewLogger() *slog.Logger` was one candidate among
  `slog.New` and `slog.Default`, an ambiguity whose advice — remove or rename all but one — nobody
  could take about the standard library; `TracerProvider` had already been special-cased for the
  same reason. Now the module's own constructors decide, a lone outside one is still selected when
  the module has none, and several outside ones wait for the module's implementations of an
  interface before they are an ambiguity — one that says to write a provider in the module or
  declare a `servo.Value`. Not breaking under the rules above: only cases that were diagnostics now
  resolve, and every example generates byte-identical output.
- **Ambiguity is headed as one, and its fix can be pasted.** Several candidates are now `ambiguous
  provider for X`, not `no provider for X`. The suggested lines spell types the way the spec file
  does — `servo.Bind[store.Store, *memory.Store]()`, bare for the spec's own package, by import
  alias where it has one — followed by the imports the spec file still needs; they used to be
  `servo.Bind[example.com/app/store.Store, *example.com/app/memory.Store]()`, which isn't Go. Two
  functions producing one type are told that servo identifies values by type alone, so renaming
  one changes nothing, and shown a distinct named type to make instead.
- **Rejected result shapes say what is wrong.** "does not match a supported result shape" is now
  followed by the specific mistake — `the error comes last — return (T, error)`, `the cleanup func
  comes before the error — return (T, func(), error)`, the offending second result, or the count.
- **A servo runtime older than the CLI is a warning, not a failure.** A capability interface the
  module's pinned runtime does not declare is skipped — no node is reported as having it, nothing
  generated calls it — and every command carries on, where all of them used to fail with
  `capability interface Readier not found`. The one-line warning names both versions and both fixes.
  A runtime declaring none of the capabilities is still an error, now saying the package did not
  load and to check go.mod and go.sum.

- **In a `go.work`, an injector's main module is its own module.** The go command marks every
  module a `go.work` lists as main, and servo took that as its own "is this the user's code" test,
  so an interface auto-bound to a sibling module's implementation, and a `//servo:config` type
  declared in a sibling had its loader written into the sibling's directory — neither of which a
  `GOWORK=off` checkout regenerates, so `servo check` in CI failed on a file `generate` had just
  written. Under the new default a sibling is a dependency like any other: its implementations are
  out of interface search, its routes and config types are not the graph's, and nothing is written
  into it. A workspace that relied on the old reading gets it back with `--scope=workspace`, or binds
  the sibling's type explicitly; without a `go.work`, nothing changes.

- **A command's flags may come before or after its arguments**, and `--` ends them. The go flag
  package stopped at the first argument, so `servo explain api.Server --json` — the order `servo
  help` itself advertised — was a usage error, and `servo check ./... --tags=prod` silently
  checked the default configuration; it now checks `prod`. Usage lines, `servo help <command>`,
  the parser and dispatch are generated from one table of commands, so a flag a command accepts
  is on its usage line by construction.
- **Arguments a command does not take are ignored out loud.** `servo generate ./cmd/app` — what a
  wire user types — still generates every injector under `--dir` with the same exit status, but
  now says on stderr that it ignored the argument and which `--dir` would narrow the run.
- **`servo new` prints complete files.** Every scaffold has its package clause and imports and
  compiles as printed; a test writes all of them into one module, roots them, generates, builds
  and vets it. `component` takes `--package` (default: the type name, lowercased). `adapter` now
  declares its settings as an unexported `//servo:config` struct, so rooting it no longer fails
  with `no provider for redisx.Config`; it connects in `Init` and tears down through `Stop` alone,
  instead of returning a cleanup func beside `Stop`. `mock-adapter` prints its package clause (`--package`, default
  `mocks`) and accepts `mockgen` for `gomock`. A name the file could not use — not an identifier,
  or unexported where a spec in another package must name it — is refused before printing.
- **`servotest.Timeout` and `servotest.Linger` refuse a parallel test.** Both change a package
  variable, and a parallel test calling one raced every test reading it — `go test -race` said so,
  and only a doc comment said not to. They now call `t.Setenv` first, so Go panics on the
  combination, in either order, before the variable is touched.
- **`explain` names the Bind behind `explicit bind`** — the interface and the Bind's position,
  `via servo.Include` when it came from an included set — and the interface behind `sole
  implementation`; `--json` gains `binds` and, for an interface query, `via` (both `omitempty`).
  **`why` names the interface a step went through**, `(as store.Store, explicit bind)`, and the
  accessor a scoped node is acquired through.
- **The tutorial runs the generator as `go tool servo`.** Chapters 2 and 14 and the tutorial's
  Makefile said `go run github.com/okian/servo/v3/cmd/servo` "works equally well"; outside this
  repository it stops at a missing `go.sum` entry for `golang.org/x/tools`. `examples/tutorial`
  declares the tool in its `go.mod`.

- **The compile-failure report names the likely causes before it mentions a bug.** It used to
  open by calling every generated file the compiler rejected "a bug in servo rather than in your
  code", which sent people to the issue tracker with their own stale mocks. It now shows the
  compiler's lines, then the usual causes, which are in the code the file was generated from — a
  `servo.Bind` or `servo.Override` type that no longer implements its interface, a provider the
  injector's package cannot refer to (under another tree's `internal/`, importing the injector's
  package, or naming another package's unexported type), a name the injector's package already
  declares — and only then invites a report.
- **The stale-file diff reads like `git diff`.** Each hunk has its line numbers, three lines of
  context and the declaration line its first change sits under, so a merge that drops one stop
  call from one rollback path shows which path, where it used to print a bare `+ _ =
  a.stopIssuer(ctx)` indistinguishable from twenty others. A diff is cut off after 400 lines,
  with a count of what was left out.
- **A suggested command repeats the flags it was run with.** `servo check --dir=services/api`
  failing in CI used to say "run `servo generate`", which run from the repository root fails
  with "run `servo init`". The stale, missing and left-over reports now name `--dir`, `--scope`
  and the build flags they were given, with the variant's own `--tags`.

- **Committed output carries no source positions.** The header comment named each provider by
  its file position, and `App.Graph()` carried the same strings, so adding a comment above any
  constructor made `servo check` fail, a standard-library provider's position moved with the Go
  patch release (`net/http/server.go:2655` under 1.27.2, `:2635` under 1.27.0), and teammates on
  different toolchains kept rewriting each other's file. The header now names the function
  (`postgres.New`, `postgres.ServoConfig`, `Values.Flags` for a supplied value), and every `Pos`
  in the generated `App.Graph()` is empty. `servo graph`, `explain` and `why` still print live
  positions.
- **`App.Graph()` is printed from the value `servo graph` builds.** The two were separate
  converters kept in step by hand, and had drifted: a scope's borrows came out in level order at
  run time and in discovery order from `servo graph --format=json`, and routes were listed group
  by group at run time and in plan order at build time. The generator now writes the literal from
  the same conversion the command uses, field by field — `nil` as `nil`, an empty slice as empty —
  so the two serialize identically except that only `servo graph` fills in positions. Borrows are
  in level order in both, and a test compiles a generated `App.Graph()` and compares its JSON with
  the command's.
- **Construction rollback is linear.** After every constructor that can fail, `New` used to
  unroll a stop call for each stoppable node built before it, which made the generated file
  quadratic in the graph — and `servo generate` pays for that twice, since it compiles what it
  writes. Each stoppable node now registers its stop method in `New`'s undo list right after it is
  built, and each failure site calls one `unwind` method that runs the list newest first. On a
  synthetic chain of 500 constructors that can all fail and all stop, the generated file went from
  136,368 lines to 12,627 and `servo generate` from 599 s and 4.0 GB to 1.9 s and 280 MB; 1,500
  such constructors now take 5.1 s and 671 MB. Each failure site no longer repeats the nodes before
  it, so two branches that each add a node can no longer merge cleanly into a file whose later
  rollback paths miss the other branch's stop call.

- **A generic instantiation is named for its type arguments.** Two instantiations of one generic
  type used to become `repoRepo` and `repoRepo2` — which one got the 2 depended on order, and
  neither said what it held. They are now `repoUser` and `repoOrder`, the trace wrappers are
  `tracedStoreUser` and `tracedStoreOrder`, and each instantiation's spans carry its arguments
  (`repo.Store[model.User]/Get`), where both used to share one span name. Non-generic names are
  unchanged.
- **The empty interface keys as `any`.** With aliases now resolved at every depth, `Repo[any]` would
  have keyed as `Repo[interface{}]`; both spellings are one type, so they share one key, and it is
  spelled `any`, which keeps node names — the ones `Report` entries and `servotest` assertions
  use — what they were.
- **`servo generate` prints resolution notes.** A capability-named method with the wrong signature,
  or a `Close` no `Stop` bridges to, is reported on stderr as a note; it never stops generation.

- **Go 1.26 is supported again: go.mod declares `go 1.26.0`, down from `go 1.27.0`.** The policy
  from this release on is the two most recent Go releases, matching Go's own support window, and
  go.mod's `go` line is the older of the two. Nothing in the module needed 1.27: the floor was
  raised for the tutorial's use of the standard library's `uuid` package, and the tutorial is its
  own module, which stays at 1.27. Every other example module declares `go 1.26.0` as well. A
  consumer on 1.26 with `GOTOOLCHAIN=local` was capped at v3.1.0, and under `auto`, `go get` raised
  their go line; neither happens now. CI tests the root module on 1.26 and 1.27 and each example
  on every version its go.mod allows. No `go mod tidy` was needed in any module.
- **A release workflow run is always on the tag itself.** The `tag` input of release.yml's
  `workflow_dispatch` is gone: a re-run after a failed upload is
  `gh workflow run release.yml --ref vX.Y.Z`, and a run on a branch is refused, because the
  provenance attestation records the run's ref and commit as the archives' source. The tag is
  checked out as `refs/tags/<tag>`, so a branch of the same name can never be built instead;
  every job checks the tag still points at the commit the run started for, and a tag on a commit
  that is not on master is refused.
- **Release builds use the newest patch release of the Go line** (`check-latest`), decided once
  by the plan job and named in the notes, and restore no Actions cache: each module comes from
  the proxy, checked against go.sum, and `go mod verify` re-hashes the module cache before
  anything is built. Every v3.4.1 build job had restored a 235 MB cache written by an unrelated
  lint job.
- **The release notes describe the archives as what they are**: an install of the CLI that still
  needs `go` on PATH, since servo loads modules through the go command — not a way to run servo
  without a Go toolchain. `go get -tool` with `go tool servo` is the recommended install.
- **CI runs `cmd/servo`'s tests as four shards** beside a job for every other package, on both
  toolchains, and merges the shards' coverage before one upload. Every `go test` has an explicit
  `-timeout` and every job a `timeout-minutes`: go test's ten-minute default per package had
  failed the nightly 36 times in a row in September and October, and `cmd/servo` alone was taking
  up to 88% of it. The module and build caches are keyed on every go.sum in the repository and
  saved only from master, where setup-go's cache had been keyed on the root go.sum alone and
  saved once; tests run with `-count=1`, since a restored cache could otherwise replay a pass for
  a `cmd/servo` test whose fixture builds against changed runtime code. A newer push to a pull
  request cancels the run of its older head.
- **The tutorial workflow runs when the root packages it builds against change** — `servo/`,
  `servoimpl/`, `servotest/`, `middleware/`, `conf/`, `internal/`, `cmd/`, `go.mod`, `go.sum` —
  not only when the tutorial does, and its `servo check` step runs `go tool servo check`, as a
  consumer's CI does.
- The docs job builds the site with `github-pages` and every gem beneath it pinned, with
  checksums, in `docs/Gemfile.lock`, and also asserts the three new pages render.

- **The README's Quick start runs as written.** It installs the generator with
  `go get -tool`, shows every file in full — including the logger the old version depended on and
  never showed, so the first `servo generate` stopped at "no provider for *logger.Logger" — gives
  the API server a `Run`, uses `servo.Main(New)` as the whole of `main`, and shows what a run and a
  Ctrl+C print. Once the logger was supplied, the old program exited 0 at once having printed
  nothing, because nothing in it implemented `Run`.
- **The comparison page is fair to wire and fx.** `google/wire` was archived in August 2025 and its
  README says it is no longer maintained; the page called it "in maintenance mode". servo and wire
  both get two instances of one type by making them two distinct types; the page said servo could
  not and wire could. fx reports a missing dependency when `fx.New` runs, before `Start`, and
  `fx.ValidateApp` checks a graph without calling any constructor; the page said fx failed at
  `app.Start()`, through a reflection-built error chain. The preface's versions of the same claims
  are corrected too, along with its "a different graph for staging than for production", which
  build variants already allow.
- **The tutorial says what to run in your own module.** Its packages compile and pass their tests
  chapter by chapter, but the binaries arrive with chapter 14's injectors, so chapters 10 to 13 ran
  programs a reader's module did not have yet. A new "Following along in your own module" section
  says to run those in the finished module; that Postgres, Redis and NATS start by name, because
  `make up` also starts a containerized copy of the service on ports 8080 and 8081, which the local
  `go run` the chapters show then found taken; and that `deploy/docker-compose.yml`, which chapter 5
  already needs, is copied from the finished module. Chapters 10, 11, 12, 15 and 17 point at it.
  The promise of a "try it" section in every chapter, kept by seven of twenty-two, now says what the
  chapters actually do. Chapter 19's workflow runs `go tool servo check`, the form a consumer's CI
  uses.
- **Chapter 3 says what `required` means**: the setting arrived from some source. A variable set to
  the empty string is present, and, the environment outranking a config file, it replaces the
  file's value. Rules about the value belong in the constructor that receives it, which is where the
  tutorial's `auth.New` now refuses a `JWT_SECRET` shorter than 32 bytes; chapter 9 shows it and
  says why 32.
- **The tutorial's probes are liveness, readiness and a report.** Chapters 1, 10 and 17 described
  `/healthz` — every dependency's own check — as the liveness probe, which restarts every replica
  when one dependency fails over. They now describe the admin port's `/livez`, `/readyz` and
  `/healthz`, and chapter 10's account of a `Run` that ignores its context describes servo's
  five-second `DefaultRunGrace` and the error that follows, where it used to describe a hang.

### Deprecated
- **`servo.RunStop`, `servo.MergeNodeResults` and `servo.LingerWindow`.** Generated code calls the
  `servoimpl` copies since v3.5.0; these remain, working, for files generated earlier. `RunStop`
  carries the not-attempted fix below. `DefaultStopBudget` and `LingerOverride` stay where they are,
  since users and `servotest` set them.

### Fixed
- **A runner's panic no longer kills the process without `Shutdown`.** Runners ran on errgroup
  goroutines with no `recover`, as did the lone runner and the emitted HTTP servers, so a panic
  exited 2 on the spot: `Shutdown` never ran, nothing was drained or flushed, and a producer's
  buffered events were lost. It is now that runner's failure, carrying the panic value and the stack:
  the other runners are cancelled, `Run` returns it, and `main`'s `Shutdown` runs as it would after
  any other failure.
- **One tenant's scoped `Run` panic no longer kills every tenant.** A scoped instance's `Run`
  goroutine was the one place in a scope with no `recover`, although constructors and `Init` had one
  and the limitations page promised a panic fails the acquire rather than the process. The panic is
  now that instance's `Run` failure, attached to its stop result and counted in `Stats().Failures`
  at eviction — the existing semantics for a scoped `Run` error.
- **A panicking `Init` rolls back.** On a level of one it unwound through `New` into `main`; on a
  shared level it killed the process from the errgroup's goroutine, after x/sync's `Wait` could
  already have returned nil, so `New` "succeeded" and `Run` began first. Neither ran the rollback, so
  a database another node had connected was never closed. It is now that node's `Init` failure, and
  takes the rollback like any other.
- **Stops are not started after the shutdown deadline.** `RunStop` started a stop whose context was
  already done and reported it abandoned the same instant, so once the deadline passed every node
  still to come began stopping at once, in the background, out of the order the graph requires,
  while `main` exited around them — a database closed under a producer still flushing into it. Such
  a stop is now not attempted: reported abandoned with an error saying so, wrapping the context's
  own. Fixed in `servo.RunStop` too, for files generated earlier.
- **`New` fails when its context ends during `Init`.** A shutdown signal arriving mid-startup used
  to leave `New` returning success once the `Init`s finished, and `Run` then bound the ports of a
  process already told to stop. `New` now starts no further level, rolls back, and returns an error
  wrapping `context.Cause(ctx)` — `servo: startup interrupted: interrupt signal received`, for which
  `errors.Is(err, context.Canceled)` holds — even when every `Init` succeeded.
- **`Run` stops waiting for a runner that ignores cancellation.** `Run` waited for every runner
  without limit, so after SIGTERM one that ignored its context kept the listeners serving until the
  supervisor killed the process, with nothing drained or stopped. Once `Run`'s context is done, each
  runner now has `servo.DefaultRunGrace` to return; past it, `Run` leaves it running and returns an
  error naming it, so `Shutdown` can begin.
- **The second-signal force exit says why and exits 128 plus the signal.** `Shutdown`'s watcher
  called a silent `os.Exit(1)`. It now writes one line to stderr naming the signal and exits the way
  a shell reports a process a signal killed: 130 for SIGINT, 143 for SIGTERM.
- **`servo.Stream` no longer leaks its reader, and a cut-short stream is no longer a success.** The
  reader was never closed, so a file or pooled connection leaked per request, and a mid-stream
  write error ended the chunked response normally. Stream now closes a reader that implements
  `io.Closer`, and a failed write after the headers aborts the response with
  `http.ErrAbortHandler`, so the client sees a truncated stream rather than a complete one.
- **JSON and XML responses encode before writing the status.** An encoding error (a `NaN`, say)
  used to send the success status with an empty body; it is now a 500 with the standard error
  envelope, logged.
- **A zero or out-of-range `servo.HTTPStatus` is a 500**, not a panic in `WriteHeader(0)` that
  dropped the connection.
- **A malformed query string is a 400.** `r.URL.Query()` drops the parse error, so with 10,001
  parameters every bound field silently became zero and the request returned 200. Routes that bind
  query fields now parse with `url.ParseQuery` and refuse what it refuses.
- **The docs say exactly what text reaches the client.** A 4xx built with `Wrap`/`Wrapf("…%w", err)`
  sends the whole chain's text, so a database constraint name could reach a caller; changing that
  would be breaking, so the HTTP reference and the examples now show an idiom that keeps internals
  out of client-visible messages.
- **`middleware.Recover` no longer appends its error to a started response.** A handler that
  panicked after writing part of a 200 got `{"error":"Internal Server Error"}` glued to the end of
  its body, which a client reads as a complete response. Recover now tracks whether the response
  has started; if it has, the panic is still logged, and the response is aborted with
  `panic(http.ErrAbortHandler)`, which net/http turns into a dropped connection (a reset stream on
  HTTP/2), so the client sees the failure. `http.ErrAbortHandler` itself still passes through
  untouched and unlogged. A `Content-Length` the handler had set is dropped from the 500 rather
  than truncating it. The writer Recover and AccessLog wrap around the handler keeps
  `http.Flusher`, `http.Hijacker`, `http.Pusher` and `io.ReaderFrom` (AccessLog's used to hide
  all four).
- **Panicking requests reach the access log.** `middleware.AccessLog` wrote its line after the
  handler returned, so with Recover outside it, as in every example, a request whose handler
  panicked was never logged. The line is now written in a defer and marked `panicked=true`, with
  the status the handler sent or 500 if it sent none — what Recover answers — before the panic
  carries on; a response cut off with `http.ErrAbortHandler` is marked `aborted=true`.
- **`middleware.Timeout`'s 503 says it is JSON.** `http.TimeoutHandler` writes its body with no
  `Content-Type`, and net/http sniffed the default `{"error":"request timed out"}` as
  `text/plain`. It is now sent as `application/json`, as is a configured `Body` that is valid
  JSON; any other body keeps the sniffed type. The godoc and the reference now spell out the
  limits `http.TimeoutHandler` brings — it buffers the whole response, hides `http.Flusher` and
  `http.Hijacker`, and leaves a handler past its limit running where `srv.Shutdown` does not wait
  for it — and what to use for streaming routes instead.
- **`middleware.Gzip` no longer costs a megabyte per response, and honours `q=0`.** A compressor
  was allocated for every response (about 1 MiB of garbage each); they are now pooled, and a
  response costs a few KiB. `Accept-Encoding` was matched as a substring, so `gzip;q=0` — a
  refusal — got gzip; it is now read with its weights, `*` included. The gzip writer also hid
  `http.Flusher` from handlers that assert it, and a flush through `http.ResponseController`
  bypassed the compressor's pending output; both now flush everything written so far.
- **`middleware.RateLimit` no longer resets slow limits.** The five-minute idle sweep dropped
  buckets that had not refilled yet, so a five-an-hour limit allowed five more requests after six
  idle minutes. A bucket is now forgotten only once it has been idle long enough to refill
  completely (`Burst/RPS`), and never under five minutes. A `RPS` of NaN or +Inf, which passed the
  sign check and turned every bucket into NaN, is a constructor error.

- **Candidates the injector cannot refer to are no longer selected.** A provider under another
  tree's `internal/`, one whose result type is unexported in another package (`func NewEvictor()
  *evictor`), and one whose package imports the injector's own package — an adapter importing the
  core package that holds the spec — used to be selected and end in a generated file the compiler
  refused, reported as a bug in servo; `check` and `doctor` passed it. Now each is set aside for
  that injector: it drops out of structural search, so it no longer makes an ambiguity of an
  implementation that is usable, and an exact or `Bind` request it alone would have answered is
  `no usable provider for X` with the reason and the fix. Parameter types are deliberately not
  judged — an argument is never written down — except a `//servo:config` value, whose loader the
  injector calls.
- **Aliases inside type arguments.** Type identity resolved an alias only at the top and through
  pointers, so `NewService(c *Cache[DBAlias])` beside `NewCache() *Cache[*DB]` reported "no provider"
  for two spellings of one type. Aliases now collapse at every depth — type arguments, generic
  aliases, elements, signatures, fields — so `servo.Root[*repo.Repo[model.UID]]()` resolves too.
- **Every `//servo:` line is judged by one pass, with one tokenizer.** The route scan skipped what
  looked like config and the config scan read only type doc comments, so `//servo:confg` on a type
  or `//servo:config` on a `var` generated nothing, exited 0, and only servo-vet complained. One pass
  now claims every line in the main module — a route on a function, `//servo:config` on a type —
  and reports the rest, at generate time and in servo-vet with the same words. Both split the line
  on any whitespace, so `//servo:get<TAB>/orders`, which generate served and servo-vet called
  unrecognized, and `//servo: get /x`, which servo-vet skipped, mean the same to both. The group-name
  grammar is defined once for `servo.Group` and the directive token.
- **Generated order no longer depends on where a module is checked out.** Trace wrappers and
  config loaders were ordered by raw absolute filename, so moving a module a `servo.Include` names
  from `../a-shared` to `../z-shared` swapped two wrappers in the committed file, and Windows' `\`
  sorted differently from `/`. They are now ordered by module path, then the module-relative slash
  path, then line and column.
- **Diagnostics come out the same way every run.** A malformed `//servo:config` was reported from
  whichever package the loader's import-map walk reached first, and several problems on one route
  line came out in map order; four different reports in ten runs. Every malformed config
  declaration is now reported, in source order, and route diagnostics are walked in pattern order
  and sorted stably, the message breaking ties.
- **Deep dependency chains cost linear memory.** The needed-by chain was copied twice per frame, so
  a 3,000-deep chain allocated about 790 MB; frames are now shared links, and a 2,000-deep chain
  resolves in under 6 MB.

- **`--dir` selects injectors; it no longer narrows the graph.** The load was the import closure of
  `--dir`'s own packages, so a run from an injector's directory — which is where the `go:generate`
  line `servo init` scaffolds runs it — saw only what that injector already imported. An interface
  the module root called ambiguous was silently auto-bound to the one implementation in reach, an
  HTTP server was emitted that bound, reported ready and answered 404 to every route declared in a
  package nothing imported yet, and `check --dir` passed both. Servo now always loads the whole main
  module, so a run from any directory resolves the graph a run from the root does; `--dir` only
  chooses which injectors are generated, checked or asked about. A `--dir` that selects none names
  where the module's injectors are. On the tutorial module, `check --dir cmd/orders` goes from 0.9s
  to 1.3s — what a run from the root costs — and the root run is unchanged.
- **A package that calls an importable injector no longer blocks it forever.** The load runs with
  the `servoinject` tag, which hides every generated file, so a `cmd/app` calling `wiring.New` failed
  to type-check, and `generate`, `check` and `doctor` refused the module for build errors while `go
  build` passed. Errors naming a member of an injector package, at the selector that names it, are
  now excused in the packages importing it, as an injector's own pre-generation errors always were;
  anything else wrong in those packages is still reported.
- **A Go release that moves the export-data format no longer breaks every command.** Servo reads
  the standard library from export data, which couples it to a `golang.org/x/tools` that knows the
  running Go's format; a newer format failed every command with x/tools' own `internal error in
  importing "context" (… export data version 5 is greater than maximum supported version 4); please
  report an issue`, naming a different package each run. Now the standard library is type-checked
  from source instead — slower, and said once on stderr — and when even that fails, the error names
  the x/tools version servo carries, the Go version in use, and both fixes: `go get
  golang.org/x/tools@latest` in the module for `go tool servo`, a newer servo for an installed
  binary. The load is also walked in a fixed order, so every report names the same package.
- **Setup mistakes are reported in the go command's words.** A `--dir` that does not exist failed as
  `fork/exec …/go: no such file or directory`; a directory outside any module, a `go.work` root
  outside its modules, and a module that does not require servo yet all reached the user as a
  missing runtime and the advice to run `servo init`, or as a runtime declaring none of the
  capability interfaces. Each now quotes the go command's reason with the step that gets past it:
  `go mod init`, running from one of the modules `go.work` lists, `go get -tool
  github.com/okian/servo/v3/cmd/servo`, `go mod tidy`.
- **A spec typo gets the type checker's error, not servo's guess.** A marker whose type name is
  mistyped, or names a package the spec file does not import — `servo.Root[*api.Sever]()`, often a
  `gopls` rename that never saw the tagged file — has no type information, and was reported as
  "must be instantiated with explicit type arguments". Servo now reports the type checker's own
  errors for the spec file (`undefined: api.Sever`), the only place they can surface since nothing
  else compiles it, and does the same for a `servo.Build` call it cannot resolve instead of saying
  to run `servo init`.
- **A `Bind` or `Override` to a type that does not implement the interface is refused at the
  marker.** It used to parse, resolve, and fail to compile inside the generated file, reported as a
  bug in servo. The message names what is wrong the way the compiler would — the missing method, a
  pointer receiver on a value type, the wrong signature — and, for a type that reads like generated
  code, says to regenerate it first: the usual cause is a mock older than its interface, because `go
  generate` runs servo before a mock generator whose directive comes later.
- **A marker the CLI and the runtime disagree about names both versions.** A spec using a marker
  the module's servo release predates, or a runtime declaring a marker this CLI has never heard of,
  read as a mistake in the spec (`unrecognized servo marker "Trace"`). Both now name the CLI's
  version, the runtime version `go.mod` resolves, and the fix — the CLI the module pins (`go tool
  servo`), or a matching runtime. The capability side of the same skew, a warning since this
  release, is now printed by every loading command.
- **Positions in vendored and nested modules no longer depend on the checkout.** A file inside the
  module root but in another module — a vendored dependency, a nested module replaced to `./tools` —
  rendered relative to the root (`vendor/github.com/…`) on a machine that vendors and as its import
  path on one that does not. The owning module now decides: the import-path form for every module
  but the injector's own.
- **The candidate scan runs once per load.** Every injector rescanned every loaded package, which
  cost a module with 44 injectors close to a second and a third of a gigabyte; the scan is shared,
  with each injector's own package rescanned for the unexported constructors only it may use, and
  the candidate set is unchanged.
- **`servo <command> -h` exits 0 with that command's help.** Every one printed the flag package's
  defaults, then `flag: help requested`, and exited 1, against the reference's own promise;
  `servo new -h` said `unknown kind "-h"`, and `servo new component --help` scaffolded a type
  named `--help`.
- **`servo --version` prints the version** instead of `generate`'s usage, twice, with exit 1.
- **`servo version` reports a replaced build.** `go tool servo` in a module replacing servo
  printed the requirement — `examples/variants` said v3.0.0 while building `../..` — and now
  prints `(devel, replaced by ../..)`, or the replacing module's version.
- **`explain` and `why` find what people type.** `*api.Server`, an interface — bound or resolved
  by sole implementation, as a root or a parameter — and a generic instantiation qualified by
  package name, `repo.Repo[model.User]`, all ended in `no node matches`; so did a bare
  `repo.Repo`. And a type that is not in the graph now gets the reason: the constructor no root
  reaches, the `Override` that puts it only in `NewTestApp`'s graph, the candidates implementing an
  interface nothing asks for, or the rejected function that returns it and the rule that rejected
  it.
- **The documented cross-compiling command works under `go tool servo`.** The reference said to
  set `GOOS` in the environment; with the generator pinned as a tool, `GOOS=linux go generate`
  builds the tool for Linux and fails with `exec format error`. It now documents
  `GOOS=linux "$(go tool -n servo)" generate`.
- **Order-assertion examples that could never pass** in the README, the servotest reference and
  the generated-API page now use `NodeName`; the servotest reference also stopped claiming nothing
  recovers a panic in a cleanup func — `RunStop` does, into the shutdown report.
- A dead link in tutorial chapter 14, to the renumbered scoped-instances chapter.
- **`servo generate` and `servo check` catch every error `go build` would stop on in a generated
  file.** The compile check kept type errors only, so the go command's own — an import of
  another tree's `internal/` package, an import cycle a generated import closes — were dropped:
  generate exited 0 and `go build` failed inside `servo_gen.go`. They are now reported the way
  `go build` reports them, alone, at the import responsible, with the cycle spelled out.
- **`servo check` compiles the generated files it finds current.** It compared bytes only, so a
  file identical to a fresh generation was current however broken it was — a `Bind` to a mock
  whose interface had grown a method generates the same broken file on every run — while `go
  build` failed. A compile error now fails `check` with `generate`'s report. A broken file exits
  1 as a stale one does, so the exit-code contract is unchanged; the extra load costs about a
  second on the tutorial.
- **A generated file the compiler could not check is no longer passed in silence.** A package
  whose imports `go list` cannot resolve yet — the first generated import of a config decoder,
  before `go mod tidy` — cannot be compiled, and `generate` used to skip it and exit 0 on a file
  `go build` then rejected. It still succeeds, but names the package with the go command's
  reason and says to run `go mod tidy` and generate again; `check` warns the same way.
- **A config loader left behind by a moved `//servo:config` type is removed.** Moving the type to
  another package, or dropping its directive, left its `servo_config_gen.go` compiling into every
  build and failing it with `undefined: Config`, while `check` and `doctor` reported the module
  current. `generate` now removes such a loader — only one carrying servo's own header, and only
  where nothing could declare the type: a directory with no other non-test Go file, which the load
  never even sees, or a package it loaded and found no `//servo:config` type in — and `check`
  refuses it as left over.
- **A `servo generate` that changes nothing writes nothing.** Every file was rewritten on every
  run, so its modification time moved, and a watcher running `servo generate && go build` on each
  change (air, in the report) rebuilt eighteen times in twenty-five seconds with nothing edited.
  A file whose bytes would not change is now left alone.
- **The override variant is resolved before anything is written.** `servo_gen.go` was written
  before the `NewTestApp` graph was resolved, so a broken `servo.Override` left a rewritten
  production file beside a missing or stale test variant. Every graph an injector writes a file
  for is now resolved and emitted first, and that graph's diagnostics say which graph they are
  about: `in the NewTestApp graph (servo.Override at cmd/app/spec.go:14:3)`.
- **`servo check` passes a CRLF checkout.** Git for Windows checks text files out with CRLF line
  endings by default, and `check` compared raw bytes, so every generated file was stale on every
  Windows checkout — with a diff of identical-looking lines and a note blaming another servo
  version. The committed side is compared with its line endings normalized. `docs/reference/cli.md`
  suggests a `.gitattributes` line (`servo*_gen*.go text eol=lf linguist-generated`).
- **The stale-file diff trims the common prefix and suffix**, as the 3.4.0 notes already claimed
  it did. It still allocated the whole quadratic table — 196 MB for a 5,000-line file, 3 GB at
  20,000, and 53 seconds for its own test. Its core is now Myers' algorithm, whose cost follows
  the number of differing lines rather than the length of the file, bounded past a thousand
  differing lines; the test asserts the bound and runs in milliseconds.
- **The build-constraint safety check refuses what it cannot decide.** Proving a spec's mirrored
  constraint stays false with `servoinject` set enumerates tag combinations, and past sixteen
  tags it answered "safe": a spec gated `servoinject || (!prod && …)` naming eighteen tags
  generated a file servo would read back as a spec. A constraint gated on `servoinject` as a
  top-level conjunct — every ordinary spec — is now decided without enumerating, however many
  tags it names, and anything else past the ceiling is refused with a message saying how to gate
  it.
- **A `servo.Value` left nil no longer reaches a constructor.** `New` delegates to `NewWith` with
  the zero `Values`, so a graph declaring `servo.Value[*sql.DB]()` — or the documented
  `servo.Value[context.Context]()` — handed every consumer a nil and crashed at startup somewhere
  inside one of them. `NewWith` now refuses a nil pointer, interface, map, func or chan value
  before anything is built, with an error naming the field and the marker and pointing at
  `NewWith`. A struct's zero value and a nil slice still pass through.
- **A missing setting no longer surfaces one crash-loop at a time.** Each `//servo:config` loader ran
  at the top of `New` and the first one to fail returned, so a deployment missing four variables
  across four packages took four restarts to learn them. `New` now runs every loader before it
  constructs anything and joins their errors.
- **Config-file decode errors no longer quote the file.** The decoder's own message was wrapped
  verbatim, and TOML and YAML decoders quote the offending value — `password =
  correcthorsebatterystaple` came back in the error. The error now names the file and the format
  but not the decoder's text, which the config reference promised all along for `secret` fields.
- **A config type named `cfg` (or `missing`, `section`, …) generates.** The loader's locals were
  fixed strings, so `type cfg struct` opened the loader with `var cfg cfg`; they now come from the
  name allocator.
- **`NameAllocator` never hands out one name twice.** It tracked base names rather than the names
  it emitted, so `pool`, `pool`, `pool2` yielded `pool2` twice, and a traced method with parameters
  `len` and `len2` was emitted with `len2` twice.
- **A user declaration in the injector package no longer collides with a generated name.** A
  user-declared `httpServer`, `defaultMaxBodyBytes`, `traced<I>` or leftover `type App` broke the
  generated file and was reported as a bug in servo; generated names now steer around the
  package's own declarations, and a name that can't move (`App` and its methods) is a diagnostic
  naming the user's declaration.
- **The tutorial no longer boots with an empty JWT secret.** `required` on a `//servo:config` field
  means *set* — an unset Helm value or compose variable arrives as an empty string, which is set —
  and golang-jwt signs and verifies with an empty key, so the tutorial service accepted tokens
  anyone could forge. As the settled config design puts invariants in constructors, the tutorial's
  `auth.New` now returns an error for a secret shorter than 32 bytes (the HS256 key size), and the
  config reference says plainly that `required` means present, that an environment variable set to
  the empty string still wins over the file, and that unknown file keys are ignored.
- **`servo graph --format=dot` breaks lines.** Labels carried a doubled backslash, so `dot` drew a
  literal `\n` on one line.
- **The README's conformance claims are true and tested.** It said "no reflect" and "the generated
  package compiles with the servo module deleted save for the small runtime" were enforced checks;
  neither was. `TestConformance` now checks every golden and the runtime packages for `reflect`, and
  that generated code imports nothing from servo's module but `servo`, `servoimpl` and `conf`, which
  is what the README now says.
- **Coverage gaps closed:** `servo.ConfigFile` gained the marker panic test every other marker had,
  and `conf` went from 72.7% to 98% with table tests over every sized integer coercion.
- **Documentation updated for this release's behaviour** — diagnostics samples taken from real runs
  (`ambiguous provider for`, Bind suggestions in spec spelling, the same-type advice), the lifecycle
  paragraphs in the README, ARCHITECTURE and the tutorial (no errgroup; `Init`'s call-scoped context;
  `Run`'s grace), the position-free committed output, the `GOOS` form for a tool-pinned servo, and
  field-naming rules, including why a field can be renamed when a same-named type joins the graph.
- **The reference pre-commit hook checked the working tree, not the commit.** With part of a
  change staged (`git add -p`), a constructor change committed without its regenerated
  `servo_gen.go` passed the hook and then failed `servo check` and the build in CI.
  `githooks/pre-commit` now sets aside everything not staged, untracked files included, runs the
  check on exactly what is being committed, and puts the working tree back exactly as it was,
  however the hook exits.
- `examples/diagnostics` had no go.sum; `go mod tidy` adds the two lines its requirements call for.
- SECURITY.md's scope named four packages; it now covers every package of the module
  (`servoimpl`, `conf`, `middleware` and `servovet` included), the generated code, the release
  archives and their attestations. codecov.yml no longer cites a stale coverage figure or says the
  Codecov token does not exist; the pull-request template lists the checks CONTRIBUTING.md asks
  for and links to it; CONTRIBUTING.md shows the regenerate commands (`UPDATE_GOLDEN=1` for the
  emitter's goldens, `--tags=prod` for the variants example) and describes every CI job; the bug
  report form asks for `servo version`, `go version` and the operating system.

- **Dead links and wrong chapter numbers in the tutorial.** Chapter 3 linked four chapters by their
  pre-renumbering file names, and chapters 1, 3, 8 and 10 named the wrong chapter in a link's text;
  chapters 11 and 12 linked `examples/tutorial/ginapi` and `grpcapi`, which live under
  `internal/transport/`, and chapter 12's `go generate` and `grpcurl` commands used the same wrong
  paths. Ten links whose text wrapped onto a second line were published pointing at `.md` files the
  site does not serve, because the site generator only rewrites a link written on one line; each now
  is.
- **Tutorial listings that did not match the module.** Chapter 9's tests called
  `auth.New(&Config{...})`, which stopped compiling when the tutorial moved to `//servo:config`;
  chapter 10 said `/readyz` reported no nodes, while the generated `Ready` reports `api.Server`; and
  chapter 15's try-it ran the service without the four settings it requires.
- **`servo explain` and `servo why` answer for a scope's key and for `context.Context`.** `explain`
  lists a scoped constructor's key and a constructor's `ctx` among a node's dependencies, but
  nothing builds either, so asking about one answered that nothing in the graph asks for it.
  `explain` now describes a scope key from the `ScopeKey` extractor that produces it, and the
  construction context as the ctx `New`, `NewWith` or `NewTestApp` is called with; `why` traces a
  path to each.
- **`explain` names each dependent once, and `why --all` prints each path once.** A constructor
  taking one node as two interfaces was listed twice among the node's dependents, and `why --all`
  printed the same path twice.
- **`[]byte` and `[]uint8` are one dependency, as are `rune` and `int32`.** Go defines `byte` and
  `rune` as aliases of `uint8` and `int32`, but servo keyed them by the spelling the source used, so
  a constructor returning `*Repo[[]byte]` did not satisfy a parameter written `*Repo[[]uint8]`
  (`no provider for *Repo[[]uint8]`), and a `servo.Value[[]byte]` reached no `[]uint8` parameter.
  Keys now spell them `uint8` and `int32` wherever they sit, the type arguments of an embedded
  field's generic type included, which also makes `struct{ Cache[DBAlias] }` and
  `struct{ Cache[*DB] }` one key. Node names in reports, `servo graph` and the generated file's
  graph follow the keys, so a committed file naming one changes when regenerated; the generated code
  keeps your types as written, and `servotest.NodeName` now agrees with servo on them.
- **A `//servo:config` alias the injector cannot import is refused.** The check that the injector
  can call a config's loader looked at the package of the type an alias stands for, not the package
  holding the directive, where the loader is generated. So an alias of a struct literal under
  another tree's `internal/`, or an alias in a package that imports the injector's own, resolved,
  and `servo generate` wrote a file that `go build` refused. Both now get the
  `config type … cannot be loaded by the injector` diagnostic a named type in that package gets.
- **The advice for two constructors of one type is valid Go for every type.** It always embedded the
  contested type, so for an unnamed interface it printed
  `type PrimaryValue struct{ interface{Get() string} }`, which does not parse, and for a pointer to
  an interface or a named pointer type a line that does not compile. Such a type is now held in a
  named field, `struct{ Value interface{Get() string} }`; every other type is still embedded.
- **A hand-written `App` method with a parenthesized receiver is no longer declared twice.** servo
  recognised a method of your own on the generated `App` only when its receiver was spelled plainly,
  so one on `(App)`, `*(App)` or `(*App)`, all legal Go, went unseen: a `Run` was not reported, and
  the generated file declared `App.Run` again, a compile error blamed on servo. Receivers are now
  read through their parentheses, so the collision is reported at your declaration, and fields step
  around such methods.
- **`servo-vet` flags a parenthesized marker call.** A call like `(servo.Build)(…)` or
  `(servo.Root[T])()` in a file without the `servoinject` tag went unreported, yet it compiles into
  the binary and panics at run time. It now gets the same diagnostic as `servo.Build(…)`.
- **`servotest.NodeName` spells `chan (<-chan T)` as servo does.** It dropped the parentheses
  go/types and the generator write, so an assertion naming a node whose type held such a channel
  never matched. What run time cannot see, parameter and result names and an interface literal's
  embedded interfaces, is now documented on `NodeName` and in the servotest reference, with a named
  type as the cure.
- **A `servo.Use` nested inside `servo.Use` is no longer called version skew.** The selector was
  reported as a marker of a newer runtime that this CLI does not know, which sent users to upgrade
  servo for a misplaced call. It now says `servo.Use is not a servo.Use selector` and names the two
  selectors that are.
- **A go command that fails building export data is reported in its own words.** With `GOCACHE=off`,
  or a `-toolexec` that fails, the load came back empty, and servo said the runtime was not imported
  and to run `servo init`; the compile check `generate` and `check` run afterwards passed, having
  compiled nothing. Both now print the go command's reason, e.g.
  `go list -export failed: build cache is disabled by GOCACHE=off`.
- **Methods declared through an alias receiver are named after their type.** For
  `type A = T; func (A) M()`, `servo list --rejected` listed `pkg.?.M` under a receiver called `?`.
  Such methods are now named, and grouped, under `T`, or `(*T)` for a pointer receiver.
- **`servo.Include` says why it cannot read what it names.** Including a function from outside the
  module, such as `os.Environ`, was told to add a `//go:build servoinject` constraint, which cannot
  help; it is now told the function is not a marker set servo can read. A marker set in a file this
  build leaves out, by a constraint beyond `servoinject` or a `GOOS` in its name, was refused as an
  argument of the wrong shape; it is now named with that file and its constraint, and a name
  declared nowhere gets the type checker's `undefined`.

### Security
- **`middleware.RateLimit` could be bypassed over IPv6 and exhausted with distinct keys.** Keyed
  per IPv6 address, one client could take a fresh bucket for every request from its /64, and the
  table had no bound, so cycling keys grew memory without limit while periodic whole-table sweeps
  stalled every request. See the RateLimit entries under Added and Changed.
- **A handler's request struct can no longer replace a value the graph injects.** The second
  handler parameter is decoded from the request whenever it points to a struct with any binding or
  `json` tag, and nothing checked whether the graph also provided that type: an injected
  `*settings.Settings` in that position was replaced by the body the client posted —
  `{"admin":true}` included — while the same type third received the singleton. `generate` now
  refuses a request struct the graph provides by a constructor, a `servo.Value`, a scope, an
  extractor or a `//servo:config` type, naming the handler, the type and its provider, with the fix:
  a request type of its own, and the provided one after it.
- **Every workflow declares its token's permissions, and the default is read-only.** The
  repository's default workflow token can write; go.yml, tutorial.yml and codeql-analysis.yml
  declared nothing, so every job ran unpinned third-party code — an unpinned `gem install` among
  it — with a token that could have pushed a `v3.99.0` tag the module proxy would serve as
  `@latest` for good. Each workflow now starts from `contents: read`, and the few jobs that need
  more say so and why: CodeQL's upload, the attestation's OIDC token, publishing the release, and
  cut-release's tag. No job that runs repository or module code holds a write token, and the
  Codecov token lives in a job that runs neither.
- **Every action is pinned to a full commit SHA**, with its version alongside, and no checkout
  leaves the token in `.git/config` (`persist-credentials: false`). setup-go is on v7, off the
  deprecated Node 20 runtime. Tools fetched without an action are pinned too: golangci-lint and
  lychee by checksum, actionlint, govulncheck and gorelease by module version.


## [3.4.1] - 2026-10-14

**Why PATCH.** No change to `servo`/`servotest`'s exported API, the CLI, or generated code: this
release is the release pipeline itself, CI and repository maintenance only.

### Added
- **A version tag publishes a GitHub release.** `.github/workflows/release.yml` runs on every
  `v*` tag push. A plan job checks that the tag is a semantic version whose major matches go.mod's
  `/vN` suffix and that this file has a section for it — a tag with no section fails before
  anything is built, with a message saying to cut the changelog first — and lists the ports from
  `go tool dist list`: every architecture the toolchain supports on Linux, macOS and Windows. One
  build job per operating system then cross-compiles `servo` and `servo-vet` for each of its
  architectures (linux/arm twice, as ARMv6 for the Raspberry Pi 1 and Zero and as ARMv7), refuses
  any binary whose stamped module version is not the tag, and archives them with the license. The
  root module's build, vet and tests run alongside. The publish job checks that every port's
  archive arrived, writes a `sha256` checksums file, and publishes the release — created as a
  draft, made public only once every asset is attached — with that changelog section as the notes.
  A `workflow_dispatch` input re-runs it for an existing tag, so a failed upload never calls for
  re-pushing a tag the module proxy has already cached. `go install
  github.com/okian/servo/v3/cmd/servo@<tag>` remains the primary install; the archives are for
  machines without a Go toolchain.

## [3.4.0] - 2026-10-14

**Why MINOR.** This release adds comment directives (`//servo:config` and the `//servo:` HTTP
routes), several markers, two packages (`middleware`, `servovet`), a subcommand, and generated
companion files; nothing it changes is breaking under the rules above. A graph declaring none of
`//servo:config`, `servo.HTTP()` or `servo.Trace` emits a byte-identical file.

### Added
- **`servo generate` compiles what it writes.** After the files are on disk, the injector
  packages — and the config packages that gained a companion — are loaded once more under the
  configuration the generated files are gated for, with the fresh files overlaid, and a compile
  error positioned inside one of them fails the run with the compiler's own message. The
  emitter formats its output but never type-checked it, which is how two of the defects fixed
  below reached `go build` with `servo generate` reporting success. The file stays on disk, since
  the broken output is what a bug report needs; a package whose imports `go list` cannot resolve
  yet (the first generated `errgroup` or decoder import, before `go mod tidy`) is skipped rather
  than judged.
- **`servo check` compares the override variant.** `servo_gen_test.go` was the one generated file
  `check` never looked at. It is now diffed exactly as `servo_gen.go` is, and a test variant left
  behind by a spec that no longer declares `servo.Override` is refused — `generate` removes such
  a leftover, with a note on stderr, since nothing would ever regenerate it. `doctor` reports the
  same three states.
- **`why` and `explain` attribute HTTP-only dependencies.** A node only a handler, a `servo.Use`
  attachment, a `servo.Extract` or `servo.HTTP()` itself pulls in descends from no `servo.Root`;
  `why` used to call it unreachable and `explain` listed no dependents. Both now name the
  declaration — `root  handler api.Order, POST /order/{category}/` — and `why --json` carries it
  as the array's first element.
- **`HTTPConfig.ReadHeaderTimeout`**, on the listener type too, passed through to
  `net/http.Server`. Zero means none, like the other timeouts; set it on anything internet-facing.
- **Every declaration kind can be superseded locally.** A `Value`, `ConfigFile`, `Scoped` (policy
  included), `HTTP` or `Extract` written after a `servo.Include` now replaces the included one,
  the rule `Bind`, `Override` and `Trace` already followed; before, each of those was "declared
  twice", so a shared set could not carry a scope policy one injector needed to change.
- **`servo.Trace[I]()`: a generated OpenTelemetry wrapper around an interface.** Declared in
  the spec, per injector, the marker makes every consumer of `I` receive an emitted decorator in
  place of whatever `I` resolves to — a `Bind`, a structural match, a `Value`, or in the test
  variant an `Override`, so `NewTestApp` traces the mock through a wrapper of its own. Each method
  with a leading `context.Context` runs inside a span named `<pkg>.<I>/<Method>`, the
  implementation receives the span's context, and a non-nil error is recorded on the span unless
  a `servo.Expected(pkg.ErrX)` option names it — a cache miss or a not-found is a result, and a
  span marked failed for every one would make any error rate computed from traces meaningless.
  `Expected` names a package-level error variable or constant and is read as syntax, never
  evaluated. Methods without a context are delegated untouched; the wrapper has no lifecycle of
  its own, so the implementation's `Init` and `Stop` run exactly as before.

  The wrapper depends on `go.opentelemetry.io/otel/trace.TracerProvider` as an ordinary graph
  node — a constructor in the user's module, with no `otel.GetTracerProvider()` fallback —
  resolved among the module's own constructors only, since the OpenTelemetry packages return that
  interface from several functions of their own. The generated file imports the stable
  OpenTelemetry API (`trace`, `codes`; nothing from `semconv`) in the *user's* module, so servo's
  own `go.mod` stays OpenTelemetry-free, the same rule the config decoders follow. Interfaces
  only: a concrete type has no seam to wrap and is refused at the declaration with the interface
  to depend on instead; a scoped implementation is refused for this release. The wrapper is a
  node everywhere the graph is visible — `explain`, `why`, `graph`, `list`, the header and
  `App.Graph()`, keyed `<I>#traced` with binding `traced` and a new `GraphNode.Wraps` field
  (`omitempty`, a schema **addition**). `servo-vet` knows the two new markers. Worked example:
  the tutorial's directive injector traces its repository, cache and broker interfaces in
  [chapter 16](docs/tutorial/16-observability.md). Reference:
  [Traced interfaces](docs/reference/tracing.md).
- **HTTP routes: `//servo:<method> <pattern>` directives and a generated server.** A route is a
  directive comment on an exported function — `func Order(ctx context.Context, req *OrderReq,
  st *store.Store) (servo.Json[*OrderResp], error)` — and a spec opts into serving the module's
  routes with the new `servo.HTTP()` marker. `servo generate` emits the `net/http.ServeMux`
  registration, typed request decoding (`path`/`query`/`header`/`form` tags with exact `strconv`
  parsing, JSON bodies under `http.MaxBytesReader`), response encoding, and the server's
  bind/serve/drain lifecycle into `servo_gen.go`; handler dependencies resolve from the graph like
  any constructor's, with the same needed-by diagnostics, and a scoped dependency is rejected with
  the widening rule's reasoning. The `servo` package gains `Json[T]`/`JSON`, `HTTPStatus` with the
  `Status` table (RFC 9110 names; a 2xx status returned in the error position picks the success
  code, 4xx messages reach the client, 5xx bodies carry canonical text while the detail goes to
  `log/slog`), and `HTTPConfig` (IP, port, optional TLS files, body limit, timeouts — constructed
  by the user's own provider). The `//servo:` comment prefix is reserved: anything under it that
  doesn't parse is a generate-time error, and pattern conflicts are caught by probing a real
  ServeMux at generate time. This deliberately reverses the "servo ships no transport" stance in
  `docs/limitations.md` — with the distinction that the transport is *emitted* into your package
  behind an explicit marker; the runtime still contains no handler, router or middleware, and stays
  stdlib-only.

  Routes split across **listener groups**, one emitted server and port per group: an optional
  trailing directive token (`//servo:get /healthz telemetry`) names the group, `servo.Group("x")`
  inside `servo.HTTP(...)` declares it per injector (so two binaries in one module serve different
  subsets), `HTTPConfig.Groups` maps each name to its `servo.HTTPListener`, and a token no spec
  declares fails generate. **Middleware** attaches in the spec — `servo.Use[T]` with optional
  `servo.Group`/`servo.Route` selectors, `T` being a graph node with a
  `Middleware(next http.Handler) http.Handler` method — wrapping in declaration order, outermost
  first, server → group → route, with every selector validated at generate time. **Extractors**
  (`servo.Extract[T]`, an `Extract(r *http.Request) (V, error)` method) turn handler parameters of
  type `V` into per-request values that fail through the same status contract before the handler
  runs; a type with both a provider and an extractor is a generate-time error.

  Responses are a sealed family, not only JSON: `servo.Response` is the base every kind implements
  (encoding lives in the runtime's `servo.WriteResponse`, so new kinds never touch the generator),
  with typed `servo.Json[T]`/`servo.Xml[T]` wrappers keeping the schema in the signature and
  `servo.Text`, `servo.HTML`, `servo.Blob`, `servo.Stream` and `servo.Redirect` constructing the
  rest — a redirect pairs with a 3xx status in the error position, which now flows through the
  success path (`< 400`). A `nil` error is always a 200; a `nil` response is always the code
  alone, making `return nil, servo.Status.NO_CONTENT` the 204 idiom.

  The commodity middleware ships in `github.com/okian/servo/v3/middleware` (stdlib-only): Recover,
  CORS, RateLimit (per-key token bucket with `Retry-After`), BodyLimit, AccessLog, Timeout,
  RequestID and Gzip — ordinary graph nodes for the existing `servo.Use`, the zero-config ones
  needing only the Use line, the configurable ones reading a `*XxxConfig` node the user's own
  provider supplies.

  The feature is visible everywhere the graph is: apps declaring `servo.HTTP()` gain a generated
  **`HTTPHandler(group string) http.Handler`** — each group's routed, middleware-wrapped handler,
  so `httptest` drives the emitted stack without binding a listener (the one addition to the
  generated public method set, and only for HTTP apps); `servo list` ends with the module's
  routing table; `servo graph --format=json` and the generated `Graph()` carry an `http` object
  (groups, routes with args, `Use` attachments, extractors — a schema **addition** consumers of
  the JSON should note); and `servo-vet` flags malformed `//servo:` comments in the editor with
  the same grammar `generate` enforces.

  Additive: an app with no `servo.HTTP()` generates byte-identical output, and the generated
  public method set keeps every signature (`Ready`'s report gains one node per group — content,
  not signature — only for apps that declare the server). Worked examples: `examples/http` (three
  listeners, end to end) and the tutorial's fourth injector `cmd/ordersservo` with
  [chapter 13, "The transport you don't write"](docs/tutorial/13-directive-transport.md) — the
  hand-written API rebuilt as directives, its middleware chain attached via `servo.Use` (the
  existing Tracer/Metrics/RateLimiter types unchanged), auth split into a middleware and a claims
  extractor, tested through `HTTPHandler` with mocked infrastructure. Reference:
  [HTTP routes](docs/reference/http.md) and [middleware package](docs/reference/middleware.md).
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
- **A request body over the limit is a 413.** The emitted adapters answered `400 malformed
  request body` for a JSON or form body that `http.MaxBytesReader` cut off, which told the client
  to fix its syntax. The `*http.MaxBytesError` is now recognised and answered with
  `413 request body too large`, through one emitted helper, before the generic 400.
- **An extractor's status under 400 is the code alone.** An extractor returns a value or an
  error, never a response, so a 2xx or 3xx it returns in the error position used to be written as
  a JSON error body under that code. It is now the status with no body, the way `nil,
  servo.Status.NO_CONTENT` is for a handler.
- **`Ready` drops for a group the moment `Shutdown` starts draining it**, instead of reporting a
  listener as bound until the process exits.
- **`servo config` prints absolute positions**, like `explain`, `why` and `list` — the table is for
  a person and their editor; only `graph` and the generated file carry module-relative ones.
- **`explain`, `why` and `config` match a short type name at a segment boundary**: `api.Server`
  no longer also matches `otherapi.Server`, which made the short form ambiguous in exactly the
  modules with enough packages to want it.
- **The stale-file diff trims the common prefix and suffix** before its quadratic core runs, so a
  one-line change in a fifteen-hundred-line generated file no longer costs a twenty-megabyte
  table, and a ten-thousand-line one no longer costs a gigabyte.
- **`examples/variants` keeps its `go:generate` directives in an untagged `servo_generate.go`**,
  through `go tool servo`, the shape `servo init` scaffolds and the only one `go generate ./...`
  ever reaches; the module gained the `tool` directive and a `go.sum` for it. The tutorial's
  `cmd/ordersservo` includes `internal/wiring` like the other three injectors instead of
  copy-pasting the shared set, which also brings the notifier root it had silently dropped.
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
- **Positions outside the module are written by import path.** A provider that lives
  in another module — `middleware.NewRecover` for every graph that uses the shipped
  middleware — used to land in `servo_gen.go` as the absolute path of the module cache or
  replace directory it was read from, so `servo check` on any machine but the one that
  ran `servo generate` reported the file stale. The generated file, `App.Graph()` and
  `servo graph` now write such a position as the declaring package's import path plus
  the file name (`github.com/okian/servo/v3/middleware/recover.go:17:6`), and a
  standard-library one as `$GOROOT/src/…`; positions inside the module stay relative to
  its root. One renderer (`load.Positions`) serves all three producers, and `servo graph`
  prints route positions through it too, where it used to print them absolute.
  Regenerate to pick up the new form — `examples/http` and the tutorial's `ordersservo`
  injector are regenerated here.
- **The loader reads the standard library from export data.** `Load` used to type-check
  every package in the import closure from source, standard library included — and since
  the runtime package imports `net/http`, that closure is ~180 stdlib packages for every
  module. It now type-checks the main module and every other module in the dependency
  tree from source (the packages that can carry markers, `Include`d sets and
  directives) and reads the standard library from the compiler's export data, completing
  each package the first pass left partial so the candidate scan still covers every
  loaded package. One type-checking session, the same identities, the same bindings; a
  load that took two seconds takes a quarter of one, and the test suite that timed out
  under `-race` in CI finishes in a few minutes. The cost is the coupling every
  go/packages-based tool has: `golang.org/x/tools` must understand the running
  toolchain's export format, so it is bumped to v0.52.0 (Go 1.27.2 moved the format to
  version 5, which v0.49.0 could not read).

### Fixed
- **A `//servo:` handler in another `package main` no longer breaks the build.** A directive on a
  function beside a sibling binary's `main.go` was served by every HTTP injector in the module,
  which meant importing a main package: `servo generate` exited 0 and `go build` failed with "is
  a program, not an importable package". Such a handler is now served only by its own package's
  injector — every other injector skips it — and a main package whose injector declares no
  `servo.HTTP()`, or that has none, is a diagnostic at the directive. Constructors in another
  main package are likewise rejected from the candidate index (`declared in another package
  main, which nothing can import`) instead of being selected into a file that could not compile.
- **An extracted parameter no longer shadows the handler's package.** An extractor producing
  `*user.User` for a handler in package `user` emitted `user, err := …Extract(r)` followed by
  `user.Me(r.Context(), user)`, which the compiler reads as a method on the local. The adapter's
  locals are now allocated clear of every import identifier the body writes.
- **A scope member no longer shadows its own package in the entry's `build()`.** The same
  collision the constructor has guarded against since the shadowing fix: `room :=
  room.NewRoom(e.key)` hid the package from every later member built through it. Members get the
  same qualified-by-package names nodes get, and the entry's allocator claims the standard-library
  identifiers `build()` hard-codes.
- **`go vet -tags=prod -vettool=servo-vet` works.** The go command forwards `-tags` to the tool
  along with the `.cfg` file it has already loaded packages for, and servo-vet's own refusal of
  the flag — which recommends exactly that invocation — refused it too. A run driven by the go
  command is now recognised and let through.
- **An `Override` can no longer fail a spec that is fine in production.** The `NewTestApp`
  resolve ran the same unused-declaration checks as the production one, so a mock that takes
  fewer things than the real implementation — the ordinary mock — tripped "declared, but nothing
  in the graph depends on" for a `servo.Value`, a `servo.Trace` or a `servo.ConfigFile` whose
  only consumer it had replaced, and `servo generate` refused the whole injector. The test
  variant now leaves such a declaration out instead: the value is omitted from `TestValues`
  (`TestValues` and `NewTestAppWith` are not emitted at all when nothing is left), the wrapper is
  not generated, the file is not read. The production graph, where the spec file is the whole
  truth, still refuses an unused declaration.
- **Extractors run before the request is decoded.** The emitted adapter decoded the JSON or form
  body first and called `Extract` after, so an unauthenticated client had its body read and
  parsed before its 401, and a malformed body answered 400 where the credential check should have
  answered 401. Extraction now comes first; the handler's arguments keep their parameter order.
- **The emitted HTTP servers are wired before `Init`.** They were constructed at the end of
  `New`, after every `Init`, so a declared group missing from `HTTPConfig.Groups` was discovered
  after the database had connected, then rolled back. Construction is pure wiring — a
  middleware's `Middleware` method is called with the same guarantee every constructor gets, that
  its dependencies' constructors have returned — so it now follows the last constructor and
  precedes the first `Init`. Its failure path uses the same rollback the `Init` phase does:
  `Shutdown` on a context the signal cannot cancel, with the report joined only when it is not
  clean. Regenerate to pick up the new shape — `examples/http` and the tutorial's `ordersservo`
  injector are regenerated here.
- **`middleware.Gzip` leaves body-less statuses alone.** A 1xx, 204 or 304 response used to get
  a `Content-Encoding: gzip` header and a gzip trailer written into a body net/http refuses.
- **`middleware.RequestID` validates an inbound id.** `X-Request-Id` is kept only when it is at
  most 128 bytes of printable ASCII; anything else is replaced rather than echoed into logs and
  response headers.
- **`servo-vet` recognises markers by their result type.** A marker is `servo.Build` or any
  `servo` function returning `Marker`, `HTTPOption`, `ScopeOption` or `TraceOption`, so a new
  marker returning an existing option type needs no change to the analyzer and cannot be
  forgotten; a test fails when the `servo` package gains an option type the analyzer does not
  know. The hand-kept name table is gone.
- **The generator's unrecognized-directive message names both directive families**, as
  `servo-vet`'s already did: `//servo:config` on a type, the route verbs on a function. It used
  to call `//servo:config` the only comment directive.
- **CI checks the tutorial's generated files on every push.** `servo check --dir
  examples/tutorial` ran only in the tutorial workflow, which is path-filtered to the tutorial,
  so a generator change could not fail on its four committed `servo_gen.go` files. The main
  workflow runs it too.

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
  are optional — chapter 14 follows on from chapter 10 whether or not you read them. The old
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
  `sessions` (added in chapter 15) or `log`, and chapter 17's router dropped the `GET /me/recent`
  route chapter 15 has the reader build. Chapter 17's constructor now matches `api/server.go`
  parameter for parameter and route for route.
- **Tutorial figures that no longer matched the module.** Chapter 18 claimed 43 tests across 14
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
  single port. New chapter 20 covers what differs between them and what deliberately does not; the
  old chapter 20 is now 20.
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
