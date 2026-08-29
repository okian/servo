# CLI commands

**Who this is for:** anyone running `servo` from a terminal, a `go:generate` directive, or a CI
job, who needs to know exactly what a command does and what it will print.

Every command below was run against
[`examples/basic`](https://github.com/okian/servo/tree/master/examples/basic) to produce the output
shown. Positions are printed as absolute paths; they have been shortened on this page to fit the
column.

## Installing

```
go install github.com/okian/servo/v3/cmd/servo@latest
```

Or without installing anything, pinned to whatever version your module already requires — which is
what a `go:generate` directive should use, so every developer and every CI runner uses the same
version:

```
go run github.com/okian/servo/v3/cmd/servo generate
```

## Invocation

```
servo [<command>] [flags] [arguments]
```

**The command is optional.** If the first argument doesn't start with `-`, it is taken as the
command name. Otherwise the command defaults to `generate`, so `servo`, `servo generate`, and
`servo --dir=cmd/api` are all generate invocations.

**Flags come before positional arguments.** Flag parsing stops at the first non-flag argument, so
`servo explain --json api.Server` works and `servo explain api.Server --json` does not — the
second form treats `--json` as a second positional argument and fails with a usage error. Both
single and double dashes are accepted (`-dir`, `--dir`), with either a space or an equals sign
(`--dir cmd/api`, `--dir=cmd/api`).

**Exit status is 0 or 1.** Success is 0. Every failure — a resolution diagnostic, a stale generated
file, an unknown command, a bad flag — is 1. There are no other exit codes, so a CI job needs no
special-casing.

**Diagnostics go to stderr, results to stdout.** `generate` and `check` print *nothing at all* on
success: silence means the work is done.

## `--dir` and injector scope

`--dir` (default `.`) is the directory the module scan starts from. Servo loads `./...` relative to
it, which means `--dir` is how you narrow the scan, not just where you point it.

An *injector* is a package containing a `servo.Build(...)` call. A module can hold several — a
monorepo's `cmd/api`, `cmd/worker` and `cmd/migrator` each wiring their own graph — and commands
split into two groups on how they handle that:

| Behaviour | Commands |
| --- | --- |
| Processes **every** injector in scope, reporting all of them | `generate`, `check`, `doctor` |
| Answers a question about **one** graph, so it asks you to disambiguate | `graph`, `explain`, `why`, `list` |
| Doesn't scan a module at all | `init`, `migrate`, `new` |

The second group errors out when more than one injector is in scope, listing the positions it
found:

```
servo: multiple injectors found in this scope — pass --dir to pick one:
  examples/basic/cmd/basic/spec.go:16:2
  examples/basic/cmd/migrator/spec.go:11:2
```

Pointing `--dir` at one injector's own directory narrows the scan to exactly that graph, because a
`package main` can never be imported by another `package main` — sibling injectors are structurally
unreachable from it.

## `generate`

```
servo generate [--dir <path>]
```

Resolves every injector found under `--dir` and writes each one's generated file. **The default
command.**

For each injector it emits `servo_gen.go` next to that injector's spec file, and additionally
`servo_gen_test.go` when the spec declares at least one `servo.Override` (see
[Generated API](generated-api.md#the-test-variant)). Both files are written atomically — via a
temp file in the same directory and a rename — so a concurrent reader never sees a half-written
file, and a process killed mid-write leaves the previous complete version in place.

Prints nothing on success. On failure it reports **every** injector that failed, not just the
first, each prefixed with its package path:

```
example.com/servodiagnostics/ambiguous: servo: 1 diagnostic(s):

ambiguous/store.go:23:6: servo: no provider for ambiguous.Store
  needed by *ambiguous.Server  ambiguous/store.go:23:6
  root                         ambiguous/spec.go:9:3

  2 types implement ambiguous.Store — add one of:
      servo.Bind[ambiguous.Store, *ambiguous.Postgres]()      ambiguous/store.go:11:6
      servo.Bind[ambiguous.Store, *ambiguous.Redis]()      ambiguous/store.go:17:6
```

Resolution has exactly two outcomes: a complete plan, or a set of diagnostics. A failed generation
never writes a partial file. See [Diagnostics](diagnostics.md) for every message it can produce.

## `check`

```
servo check [--dir <path>]
```

Verifies that every injector's committed `servo_gen.go` is byte-identical to what a fresh
generation would produce. Never writes anything.

This is the CI command. A constructor signature change without a matching re-run of
`servo generate` fails here instead of shipping a stale generated file. It reports every stale
injector in one run, with a unified diff for each:

```
servo check: cmd/migrator/servo_gen.go is stale — run `servo generate`
--- cmd/migrator/servo_gen.go (committed)
+++ cmd/migrator/servo_gen.go (fresh)
-func (a *App) Report() servo.StartupReport { // hand-edited
+func (a *App) Report() servo.StartupReport {
```

The diff is `+`/`-` lines only — no hunk headers, no unchanged context — which for a generated file
is usually the whole story in two lines.

A missing generated file is reported distinctly from a stale one:

```
servo check: cmd/basic/servo_gen.go does not exist — run `servo generate`
```

Two things `check` does not do. It does not compare `servo_gen_test.go`, so an override variant
that has drifted is not reported here (it will still be compiled and run by `go test`). And it
does not verify that the generated file is committed to VCS — that is `doctor`'s job.

## `graph`

```
servo graph [--dir <path>] [--format=text|json|dot|mermaid]
```

Exports one injector's resolved graph. `text` is the default; edges always point from a dependent
to its dependency.

**`text`** — grouped by level, which is the unit of Init concurrency:

```
── Level 1 ──
  *servobasic/logger.Logger
      deps: none
      capabilities: Finalizer
      binding: sole candidate
      pos: logger/logger.go:10:6
── Level 2 ──
  *servobasic/postgres.DB
      deps: *servobasic/logger.Logger
      capabilities: Initializer, Finalizer, Healther
      binding: explicit bind
      pos: postgres/postgres.go:13:6
── Level 3 ──
  *servobasic/api.Server
      deps: *servobasic/postgres.DB
      capabilities: Runner, Drainer, Finalizer, Readier
      binding: sole candidate
      pos: api/api.go:15:6
```

**`json`** — the stable machine format, and the same schema the generated `App.Graph()` serialises
to. It is `servo.Graph`, documented field by field in
[servo package](servo-package.md#graph-and-graphnode):

```json
{
  "nodes": [
    {
      "type": "*example.com/servobasic/logger.Logger",
      "level": 1,
      "deps": [],
      "capabilities": ["Finalizer"],
      "binding": "sole candidate",
      "pos": "logger/logger.go:10:6"
    }
  ]
}
```

**`dot`** — Graphviz, `rankdir=BT`, nodes filled by level and labelled with their capabilities:

```
servo graph --format=dot --dir cmd/api | dot -Tsvg > graph.svg
```

**`mermaid`** — a `graph BT` flowchart with a `classDef` per level, for pasting into a README or a
docs page:

```
graph BT
  n0["*example.com/servobasic/logger.Logger"]:::level1
  n1["*example.com/servobasic/postgres.DB"]:::level2
  n2["*example.com/servobasic/api.Server"]:::level3
  n1 --> n0
  n2 --> n1
  classDef level1 fill:#bfdbfe;
  classDef level2 fill:#93c5fd;
  classDef level3 fill:#60a5fa;
```

### Scopes in the graph output

Every format separates the app's singletons from each [scope](scopes.md)'s members, because a
scoped node's level counts from its own scope's floor rather than the app's.

`text` prints a block per scope, with its policy and what it borrows:

```
══ example.com/servoscoped/chat.RoomKey ══
  linger: 30s   max: 10000
  accessors: example.com/servoscoped/chat.Rooms
  borrows:   *example.com/servoscoped/logger.Logger
── Scope level 1 ──
  *example.com/servoscoped/chat.RoomLog
      ...
```

`json` adds a `scope` field to each scoped node and a top-level `scopes` array. Both are
`omitempty`, so an app with nothing scoped emits exactly the JSON it always did:

```json
{
  "nodes": [
    { "type": "*chat.Room", "level": 2, "scope": "chat.RoomKey", "…": "…" }
  ],
  "scopes": [
    {
      "key": "chat.RoomKey",
      "linger": "30s",
      "max": 10000,
      "accessors": ["chat.Rooms"],
      "members": ["*chat.RoomLog", "*chat.Room"],
      "borrows": ["*logger.Logger"]
    }
  ]
}
```

`dot` puts each scope in a dashed `cluster_scope<n>` subgraph, and `mermaid` in a labelled
`subgraph`, both with the key type drawn as its own node. A consumer's edge to an accessor
interface is routed to that key: an accessor is generated code, not a resolved node, so the edge
would otherwise dangle.

An unrecognised format is an error: `servo graph: unknown --format "svg" (want text|json|dot|mermaid)`.

## `explain`

```
servo explain [--dir <path>] [--json] <type>
```

Answers, for one node: which provider was selected and why, where that provider is declared, what
it depends on, what depends on it, its lifetime, its level, and its detected capabilities. Scoped
nodes can be asked about too, and the `lifetime:` line is where the difference shows:

```
$ servo explain chat.Room
*example.com/servoscoped/chat.Room
  provider:     chat.NewRoom (chat/chat.go:74:6)
  binding:      sole candidate
  lifetime:     scoped — one per example.com/servoscoped/chat.RoomKey, linger 30s, max 10000
  level:        2
  depends on:   example.com/servoscoped/chat.RoomKey, *example.com/servoscoped/chat.RoomLog
  depended on:  (acquired via example.com/servoscoped/chat.Rooms)
  capabilities: Initializer, Runner, Drainer, Finalizer
```

```
$ servo explain api.Server
*example.com/servobasic/api.Server
  provider:     api.New (api/api.go:15:6)
  binding:      sole candidate
  lifetime:     singleton — one per process, built by New
  level:        3
  depends on:   *example.com/servobasic/postgres.DB
  depended on:  none
  capabilities: Runner, Drainer, Finalizer, Readier
```

`binding` is one of three values, and it is the answer to "why this provider":

| Value | Meaning |
| --- | --- |
| `explicit bind` | A `servo.Bind` (or `Override`) named this concrete type |
| `sole candidate` | Exactly one function in the module returns this exact type |
| `sole implementation` | The parameter is an interface, and exactly one candidate in the main module implements it |

**How `<type>` is matched.** An exact match against the node's full type string wins. Failing
that, servo looks for a node whose type string *ends with* the argument — so `api.Server` finds
`*example.com/servobasic/api.Server` without you typing the import path. Two consequences worth
knowing:

- A leading `*` never matches a suffix. `servo explain '*api.Server'` fails with
  `servo: no node matches "*api.Server"`; write `api.Server`, or the full
  `*example.com/servobasic/api.Server`.
- An ambiguous suffix is an error rather than a guess:
  `servo: "Server" matches multiple nodes, be more specific: ...`.

`--json` prints the same information as an object with `type`, `provider`, `pos`, `binding`,
`lifetime`, `level`, `depends_on`, `depended_on` and `capabilities`, plus `scope` when the node is
scoped (omitted when it isn't).

## `why`

```
servo why [--dir <path>] [--json] <type>
```

Answers "why is this in my binary at all": the shortest path from a root down to the named node.

```
$ servo why logger.Logger
root  *example.com/servobasic/worker.Consumer
  -> *example.com/servobasic/logger.Logger
```

The search runs breadth-first from every root at once, so with several roots you get *a* shortest
path — not necessarily one through the root you had in mind. In the example above `logger.Logger`
is reachable from both `api.Server` (via `postgres.DB`, two hops) and `worker.Consumer` (one hop),
and the shorter one is reported.

A path into a scope is reported through the accessor edge without naming it. Asking why a scoped
node is present prints its consumer directly:

```
$ servo why --dir examples/scoped example.com/servoscoped/chat.Room
root  *example.com/servoscoped/api.Server
  -> *example.com/servoscoped/chat.Room
```

`api.Server` takes `chat.Rooms`, not `*chat.Room`. The accessor is generated code rather than a
resolved node, so — as in [`graph`](#graph)'s `dot` and `mermaid` output — the edge is collapsed
onto the scoped type it hands out, and the answer is about reachability rather than about the
parameter list.

A node that resolved but isn't reachable from any root is reported as such:
`servo why: <type> is not reachable from any root`. Type matching works exactly as in
[`explain`](#explain). `--json` prints the path as an array of type strings.

## `list`

```
servo list [--dir <path>] [--rejected] [--all] [--json]
```

Dumps the candidate index — every function servo accepted as a possible provider, with its
position:

```
$ servo list
api.New                        api/api.go:15:6
logger.New                     logger/logger.go:10:6
mockstore.New                  mockstore/mockstore.go:29:6
postgres.New                   postgres/postgres.go:13:6
queue.NewOrdersAccount         queue/queue.go:24:6
queue.NewAuditAccount          queue/queue.go:28:6
relay.New                      relay/relay.go:24:6
worker.New                     worker/worker.go:13:6
```

`--rejected` is the higher-value mode, and the first thing to reach for when you wrote a
constructor and servo doesn't see it. It lists every function that *looked* like it might be a
provider and the rule that excluded it:

```
$ servo list --rejected
api.(*Server).Ready            api/api.go:19:18       method, not a function
api.(*Server).Run              api/api.go:29:18       method, not a function
postgres.(*DB).Init            postgres/postgres.go:21:14  method, not a function
```

Every reason string is enumerated in
[Resolution rules](resolution.md#rejection-reasons). Functions with **no** results at all are
neither accepted nor rejected — they aren't trying to construct anything, and listing them would
bury the real answers under every helper in the module.

`--all` includes stdlib and third-party packages. Both modes default to the main module only,
because someone asking why their constructor wasn't picked up is never asking about
`unicode.ToLower`. `--json` prints `{name, pos}` objects, or `{name, pos, reason}` with
`--rejected`.

## `init`

```
servo init [--dir <path>]
```

Scaffolds `servo_spec.go` in `--dir`, with the build tag, the `go:generate` directive, and the
package clause already correct:

```go
//go:build servoinject

package main

//go:generate go run github.com/okian/servo/v3/cmd/servo generate

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		// servo.Root[*yourpkg.YourType](),
	)
}
```

The package name is taken from any existing `.go` file in the directory, falling back to `main` —
the usual case of a spec file landing next to a `cmd/*/main.go`. Prints
`servo init: wrote <path>`, creating the directory if it doesn't exist.

It refuses to overwrite: `servo init: <path> already exists`.

## `doctor`

```
servo doctor [--dir <path>]
```

Diagnoses setup problems before `go generate` is ever run, across every injector in scope. Every
line is `[OK  ]`, `[FAIL]`, or `[WARN]`, and any `FAIL` makes the command exit 1 with
`servo doctor: problems found`.

```
$ servo doctor --dir examples/basic
servo doctor:
  [OK  ] no build errors outside the injector(s)
  -- example.com/servobasic/cmd/basic --
  [OK  ] spec file found at cmd/basic/spec.go:16:2, correctly gated by the servoinject build tag
  [OK  ] generated file present: cmd/basic/servo_gen.go
  [OK  ] generated file matches a fresh generation
  [OK  ] generated file is tracked by git
  -- example.com/servobasic/cmd/migrator --
  [OK  ] spec file found at cmd/migrator/spec.go:11:2, correctly gated by the servoinject build tag
  [OK  ] generated file present: cmd/migrator/servo_gen.go
  [OK  ] generated file matches a fresh generation
  [OK  ] generated file is tracked by git
```

What each check means:

| Check | Fails when |
| --- | --- |
| Module loads | `go/packages` can't load the module at all |
| Spec file found | No `servo.Build(...)` call, or one in a file without the `servoinject` constraint |
| No build errors outside the injector(s) | Some *other* package doesn't type-check. Errors inside an injector's own package are deliberately ignored: before the first generation, `main.go` legitimately references a `New` that doesn't exist yet |
| Generated file present | `servo_gen.go` is missing next to the spec |
| Generated file fresh | Same comparison [`check`](#check) makes |
| Tracked by git | A `[WARN]`, never a `FAIL` — best-effort, so no git, no repo, or a different VCS just means "can't tell" |

The generated file *should* be committed, which is what that last check is nudging: a checkout
should build without anyone having to run `servo generate` first.

## `migrate`

```
servo migrate [--dir <path>]
```

Reads v1-style `Register(X{}, N)` calls and prints a report plus a v3 spec skeleton to stdout.
Nothing is written to disk and no function bodies are rewritten.

The report exists to surface information, not to claim a migration is automatic. v1 components
took no constructor parameters — they found each other through package-level globals — so there is
no dependency graph to derive a real order from. What the report gives you is the old `order`
values for review, with duplicates flagged as likely latent ordering bugs:

```
servo migrate report:
  v1 has no constructor parameters, so there is no real dependency graph to derive
  an order from — this only surfaces the OLD order values for review.

  order=1    Logger                         legacy.go:21:2
  order=2    DB                             legacy.go:22:2  <- shares this order with another service: a likely latent ordering bug
  order=2    Cache                          legacy.go:23:2  <- shares this order with another service: a likely latent ordering bug
  order=3    Server                         legacy.go:24:2
```

Then a spec skeleton with one `servo.Root` per registration, each annotated with the order it used
to carry:

```go
//go:build servoinject

package main

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Logger](), // was order=1
		servo.Root[*DB](), // was order=2
		servo.Root[*Cache](), // was order=2
		servo.Root[*Server](), // was order=3
	)
}
```

Turning globals into constructor parameters is the part that needs human
judgement, and it is the part servo deliberately doesn't guess at. With no registrations found it
says so and exits 0.
[`examples/migrate`](https://github.com/okian/servo/tree/master/examples/migrate) is a worked
example.

## `new`

```
servo new component <Name>
servo new adapter <pkgname>
servo new mock-adapter <moq|mockery|gomock> <GeneratedTypeName>
```

Prints a scaffold to stdout — never writes a file, never takes `--dir`, and never imports `servo`.
Redirect it where you want it.

**`component`** prints a type, a constructor, and all seven capability methods commented out,
ready to uncomment whichever the component actually needs.

**`adapter`** prints the shape of a third-party wrapper: a `Config`, a `Client`, a
`func New(cfg Config) (*Client, func(), error)` constructor returning a cleanup func, and `Stop`
and `Health` methods.

**`mock-adapter`** prints the small hand-written file a generated mock needs before it can be a
provider. Each of the three tools needs a different fix, for a different reason — `moq` generates
no constructor at all, while mockery's and gomock's constructors take a per-test value the graph
has no way to supply — and this subcommand knows which is which. The full explanation, with
runnable examples, is in the README's
[Mocking section](https://github.com/okian/servo/blob/master/README.md#mocking).

An unknown kind or tool is an error naming the valid set.

## `servo-vet`

```
go run github.com/okian/servo/v3/cmd/servo-vet ./...
```

A standalone [`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis) analyzer (named
`servovet`) for the two servo mistakes the compiler cannot catch.

**A marker call without the build tag.** Calls to `servo.Build`, `Root`, `Bind`, `Override`,
`Scoped`, `Linger` or `Max` in any file that doesn't carry a build constraint requiring
`servoinject`. The markers panic when actually executed, so such a call compiles straight into your
real binary and panics at runtime. This catches it in the editor instead:

```
spec.go:9:2: servo: servo.Build called in a file without a `//go:build servoinject` constraint —
it will compile into the real binary and panic at runtime; run `servo init` or add the tag
```

**A `ScopeKey` method with a reachable receiver.** servo calls that method on a typed nil, because
the key has to be known before an instance can be chosen, and no signature can say "never
dereferences the receiver":

```
chat/chat.go:91:6: servo: ScopeKey must not name its receiver — servo calls it on a typed nil,
so a receiver the body can reach is a nil dereference in production; write
`func (*T) ScopeKey(...)`
```

The check is narrowed to methods that really are key extractors — `context.Context` first, `(K,
error)` out, `K` a defined non-interface type — so an unrelated method that happens to share the
name is left alone. `servo generate` makes both checks too; the analyzer runs them everywhere,
including in packages no injector has reached yet.

Because it's a `singlechecker` binary, it plugs into anything that speaks `go vet`'s analyzer
protocol, including `golangci-lint`'s custom-analyzer support and most editor integrations.

## Wiring it into a project

**`go:generate`** — what `servo init` scaffolds, pinned to your module's own servo version:

```go
//go:generate go run github.com/okian/servo/v3/cmd/servo generate
```

**CI** — run `check`, not `generate`, so a stale file fails the build instead of being quietly
fixed on the runner.
[`.github/workflows/go.yml`](https://github.com/okian/servo/blob/master/.github/workflows/go.yml)
is a reference workflow doing exactly that.

**Pre-commit** — [`githooks/pre-commit`](https://github.com/okian/servo/blob/master/githooks/pre-commit)
runs the same check locally. It is not enabled by default; turn it on per clone with:

```
git config core.hooksPath githooks
```
