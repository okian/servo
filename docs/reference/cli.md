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

## Build flags

Servo resolves your graph by loading your module the same way the go command does, so it takes the
go build flags that decide *which files and packages exist*:

```
--tags tag,list      additional build tags to consider satisfied during the load
--mod mode           module download mode: readonly, vendor or mod
--modfile file       read an alternate go.mod instead of the one in the module root
--overlay file       read a JSON config file providing an overlay for build operations
```

Same names, same syntax, same meaning as `go build`. `--tags` takes a comma-separated list (the
deprecated space-separated form the go command still accepts works here too), and repeating the flag
takes the last one, exactly as the go command does.

They are accepted by the seven commands that load packages — `generate`, `check`, `graph`,
`explain`, `why`, `list`, `doctor` — and by no others. `init` and `new` write files without loading
anything. `migrate` walks the tree with `go/parser` and never evaluates a build constraint at all,
so a `--tags` there would be a lie: it reads v1 `Register` calls whether or not a tag would have
excluded the file.

The flags that only change what the compiler or linker *emits* are deliberately absent —
`-race`, `-cover`, `-trimpath`, `-pgo`. Servo runs neither tool. (`-race` does activate the `race`
build tag, so `--tags=race` expresses that case explicitly.)

`GOOS` and `GOARCH` have no flag because the go command has none either; set them in the
environment, as you would for `go build`:

```
GOOS=linux servo generate
```

Note what that does *not* do: `GOOS` is not a variant axis. Generating under a different `GOOS`
rewrites the same `servo_gen.go` in place with the cross-compiled graph, under the same constraint —
so if your providers differ by platform, that overwrites the graph for your host. See
[limitations](../limitations.md#build-variants-only-cover-build-tags-not-goosgoarch).

`--modfile` and `--overlay` take paths relative to **your** working directory, not to `--dir`, so
they behave the way the same flags behave on `go build`.

### `GOFLAGS`

**Servo does not take its tags from `GOFLAGS`, and this is deliberate.** It is the one place where
"same as the go command" has to yield: `go build` produces a binary you throw away, while
`servo generate` produces a file you commit. A `GOFLAGS=-tags=prod` in a shell profile or a CI image
would resolve a different graph and write it into the file named for the *default* configuration —
prod-only providers under a `//go:build !servoinject` constraint claiming to compile everywhere,
with nothing in the diff to explain it. What servo commits has to be a function of your repository
and the flags you actually typed, so pass `--tags` explicitly.

Every other `GOFLAGS` entry still reaches the go command untouched — `-mod`, `-modfile` and the rest
behave exactly as they would for `go list`, because servo never overrides them.

### Tags servo rejects

A tag that can't distinguish one build from another can't gate a variant, so servo rejects it up
front rather than letting it fail somewhere unhelpful:

| Rejected | Why |
| --- | --- |
| A `GOOS` or `GOARCH` name (`linux`, `arm64`, `js`, …) | Passing one through `-tags` doesn't select a platform, it *adds a second one*, and the build then fails inside the standard library with `GOOS redeclared in this block` and nothing pointing back at servo. Set the environment variable instead |
| `unix`, `cgo`, `gc`, `gccgo`, `boringcrypto` | The toolchain already sets these for the build they describe, so they're true without being passed and can't distinguish one variant from another. (`race`, `msan` and `asan` are *not* in this group: they're set only when `-race`/`-msan`/`-asan` is passed, so they can gate a variant like any other tag) |
| `go1.21` and friends | The toolchain sets a tag for its own release and every earlier one |
| `ignore` | The ecosystem's universal "never build this file" tag, used by the standard library's own generator sources. Passing it doesn't select anything — it compiles every deliberately-excluded file in your module *and* the standard library, and the failure lands somewhere in `$GOROOT` |
| Anything with a character outside letters, digits, `_` and `.` | No `//go:build` line could name it. The go command accepts such a tag silently, which is exactly why servo has to be the one to say so |
| An uppercase tag | Servo's own rule, not Go's. Variant file names are derived from the tag set, and `prod` and `Prod` name the same file on a case-insensitive filesystem |

## Build variants

Passing `--tags` changes the graph servo resolves — providers behind `//go:build prod` become
visible, and providers behind `//go:build !prod` disappear. A generated file describing that graph
references types that only exist in that configuration, so it must not compile in any other. Servo
handles this by giving each configuration its own file.

**The generated file's constraint is your spec file's constraint, mirrored.** Servo negates the
`servoinject` term and leaves everything else exactly as you wrote it, then conjoins the tags the
graph was resolved under:

| Spec file's constraint | Flags | Generated file | Its constraint |
| --- | --- | --- | --- |
| `servoinject` | *(none)* | `servo_gen.go` | `!servoinject` |
| `servoinject` | `--tags=prod` | `servo.prod_gen.go` | `!servoinject && prod` |
| `servoinject && !prod` | *(none)* | `servo_gen.go` | `!servoinject && !prod` |
| `servoinject && prod` | `--tags=prod` | `servo.prod_gen.go` | `!servoinject && prod` |
| `servoinject && !prod` | `--tags=dev` | `servo.dev_gen.go` | `!servoinject && !prod && dev` |

Servo never invents a negation. If two variants have to exclude each other — a default build and a
`prod` build, say — you write that in the spec files themselves, in Go's own constraint language:

```go
// cmd/app/spec.go
//go:build servoinject && !prod

// cmd/app/spec_prod.go
//go:build servoinject && prod
```

Now `servo generate` sees only the first spec and writes `servo_gen.go` gated `!servoinject &&
!prod`; `servo generate --tags=prod` sees only the second and writes `servo.prod_gen.go` gated
`!servoinject && prod`. The two coexist, `go build` picks the first, `go build -tags=prod` picks the
second, and neither run touches the other's file.

This is also why a variant needs its own spec file rather than a flag on one shared spec: a
`servo.Bind[store.Store, *postgres.PG]()` naming a type that only exists under `prod` cannot
type-check in the default configuration, so no single spec file could describe both.

**Servo refuses to write two variants that don't exclude each other.** Deriving the constraint from
your spec file is what frees servo from having to track the variant set, but nothing stops you from
generating two variants that overlap — the plain `//go:build servoinject` that `servo init`
scaffolds, generated a second time with `--tags=prod`, gives `!servoinject` beside
`!servoinject && prod`, and `go build -tags=prod` would compile both. `generate` and `check` both
detect that and stop, naming the two files and the fix:

```
servo: servo.prod_gen.go and servo_gen.go would both compile in the same build

  servo_gen.go:      //go:build !servoinject
  servo.prod_gen.go: //go:build !servoinject && prod
```

Detecting it is servo's job; resolving it is not. Rewriting the sibling file to insert the
`&& !prod` that would fix it would make generation depend on which files happen to be in your
working tree, so servo reports and stops.

**A configuration you never generated has no generated file.** Constraints are ordinary Go, so this
behaves exactly as hand-written conditional compilation does. Spec files gated `servoinject && prod`
and `servoinject && dev` with no default leave a plain `go build` with no `New` at all —
`undefined: New`, the same error a missing `//go:build` case would give you anywhere else. Writing
the default variant's spec as the catch-all (`servoinject && !prod && !dev`) is what makes an
unrecognised tag fall back to it. Asking for two variants at once (`go build -tags=prod,dev`) is a different matter: `prod` and `dev`
gated only against the *default* still overlap each other, so servo refuses to generate the second
one until each spec excludes all the others — `servoinject && prod && !dev` and
`servoinject && dev && !prod`. With more than two configurations that is O(n²) terms by hand, which
is the honest cost of the model; servo will not let you skip it silently.

**Most tag usage needs no variant at all.** If `//go:build prod` and `//go:build !prod` both define
`func New() *DB`, the resolved graph is identical either way, so one `servo_gen.go` already works.
You only need a second variant when the graph genuinely differs.

### Variant file names

`servo_gen.go` and `servo_gen_test.go` when there are no tags — unchanged, so nothing moves in an
existing project. With tags, the canonical (sorted, deduplicated) tag set becomes a dot-separated
segment: `servo.prod_gen.go`, `servo.integration-prod_gen.go`, and the `servo.prod_gen_test.go`
override variant alongside.

Both separators are load-bearing, and both are the opposite of the obvious choice:

- **A dot before the tags, not an underscore.** Go derives an *implicit* `GOOS`/`GOARCH` constraint
  from a file's underscore-separated suffix, so a `servo_gen_linux.go` would be generated,
  committed, and then silently ignored on every non-Linux machine. `go/build` cuts the name at the
  first dot before looking for underscores, which makes everything after `servo.` invisible to that
  rule for any tag, with no list of reserved names to keep current.
- **A dash between tags, not an underscore.** `_` is legal inside a build tag, so joining with it
  would map the tag sets `{a_b}` and `{a, b}` to the same file and lose one of them. `-` cannot
  appear in a tag at all, so the name stays unambiguous.

### Checking variants

`check` and `doctor` inspect the variant matching the flags you give them, so a module with two
variants gets one run each:

```
servo check
servo check --tags=prod
```

In a multi-injector module, an injector whose spec is gated out of the current configuration gets no
generated file, so its `main.go` calls a `New` that nothing supplies. `generate` and `check` stay
quiet about that — excluding an injector from a configuration is a legitimate thing to do — but
`doctor` reports it, because the alternative is an `undefined: New` from the compiler much later:

```
$ servo doctor --tags=prod
  [FAIL] example.com/app/cmd/worker holds a spec file this configuration cannot see, so nothing
         generates its New — either give it a variant for these flags, or gate the package itself
         out of this build
```

## `generate`

```
servo generate [--dir <path>] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]
```

Resolves every injector found under `--dir` and writes each one's generated file. **The default
command.**

For each injector it emits its generated file next to that injector's spec file — `servo_gen.go`,
or a [variant name](#variant-file-names) when `--tags` is given — and additionally the
`servo_gen_test.go` override variant when the spec declares at least one `servo.Override` (see
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
servo check [--dir <path>] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]
```

Verifies that every injector's committed generated file is byte-identical to what a fresh
generation would produce. Never writes anything. With `--tags` it checks that configuration's
[variant](#build-variants), so a module with several needs one run per variant.

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
servo init [--dir <path>] [--tags tag,list]
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

With `--tags`, it scaffolds a [variant](#build-variants) instead: `servo init --tags=prod` writes
`servo_spec_prod.go` gated `//go:build servoinject && prod`, whose `go:generate` line carries the
same flags. It then names any sibling spec still visible under those tags, since leaving the default
spec ungated is what makes two variants collide:

```
servo init: wrote cmd/app/servo_spec_prod.go
servo init: servo_spec.go is also visible with --tags=prod, so both would generate a file and the two would compile together.
            Narrow it to `//go:build servoinject && !prod` (or otherwise exclude the new tags) before running servo generate.
```

## `doctor`

```
servo doctor [--dir <path>] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]
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
| Generated file present | The generated file — `servo_gen.go`, or the [variant](#build-variants) matching `--tags` — is missing next to the spec |
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

> **Build tags and `servo-vet`.** Run standalone, it analyses only the default configuration, and the
> `-tags` flag it inherits from `go/analysis` is a documented no-op ("no effect (deprecated)") — so
> `servo-vet -tags=prod ./...` silently covers nothing extra. To check a tagged configuration, drive
> it through the go command, which does understand build flags:
>
> ```
> go vet -tags=prod -vettool=$(which servo-vet) ./...
> ```

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

If the injector is a [variant](#build-variants), `servo init --tags=prod` scaffolds it already
gated — `//go:build servoinject && prod`, with a matching `go:generate` line — and names any
existing spec that does not yet exclude the new tags, which is the step everyone forgets.

**CI** — run `check`, not `generate`, so a stale file fails the build instead of being quietly
fixed on the runner. **A project with variants needs one `check` per configuration**, since each
variant has its own generated file and `check` only inspects the one its flags select:

```
servo check
servo check --tags=prod
```

[`examples/variants`](https://github.com/okian/servo/tree/master/examples/variants) is a working
two-variant project, checked both ways in this repository's own CI.
[`.github/workflows/go.yml`](https://github.com/okian/servo/blob/master/.github/workflows/go.yml)
is a reference workflow doing exactly that.

**Pre-commit** — [`githooks/pre-commit`](https://github.com/okian/servo/blob/master/githooks/pre-commit)
runs the same check locally. It is not enabled by default; turn it on per clone with:

```
git config core.hooksPath githooks
```
