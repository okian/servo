# Diagnostics

**Who this is for:** anyone looking at a message servo just printed and wanting to know what to
change.

Servo's position on failure is that a missing dependency, an ambiguous binding, or a cycle is a
build error with a source position — never a runtime panic and never a guess. This page lists every
message it can produce, grouped by the stage that produces it.

All of them go to stderr, and all of them exit 1 — with one deliberate exception, `servo-vet`'s
[refusal of `-tags`](#-tags-is-refused), which exits 2 because it is the tool declining to run
rather than a finding about your code.

## Reproducing them yourself

[`examples/diagnostics`](https://github.com/okian/servo/tree/master/examples/diagnostics) is eight
small, permanently broken fixtures — one per failure mode — each runnable on its own:

```
go run ./cmd/servo generate --dir examples/diagnostics/missing
go run ./cmd/servo generate --dir examples/diagnostics/ambiguous
go run ./cmd/servo generate --dir examples/diagnostics/cycle
go run ./cmd/servo generate --dir examples/diagnostics/widening
go run ./cmd/servo generate --dir examples/diagnostics/crossscope
go run ./cmd/servo generate --dir examples/diagnostics/extractor
go run ./cmd/servo generate --dir examples/diagnostics/undeclared
go run ./cmd/servo generate --dir examples/diagnostics/noscopekey
```

Every example below is that command's real output, with two consistent abbreviations for width:
absolute file paths are shortened to their module-relative form, and fully qualified type names to
their last two segments — `missing.Store`, not `example.com/servodiagnostics/missing.Store`. The
tool always prints the full path.

## Anatomy of a resolution diagnostic

```
example.com/servodiagnostics/missing: servo: 1 diagnostic(s):

missing/server.go:11:6: servo: no provider for missing.Store
  needed by *missing.Server  missing/server.go:11:6
  root                       missing/spec.go:9:3
```

Four parts, worth reading in this order:

- **The package path prefix** — which injector failed. Only `generate` adds it, because only
  `generate` processes several injectors in one pass. Every failed injector is reported, not just the
  first.
- **`N diagnostic(s)`** — resolution collects every failure it can before giving up, so one run
  usually tells you everything.
- **`file:line:col:`** — the position, in the form editors and CI annotations already parse. It is
  the declaration site of the component that couldn't have its dependency satisfied.
- **The chain** — one `needed by` line per consumer, immediate consumer first, then exactly one
  `root` line pointing at the `servo.Root[…]()` call this traversal descended from. That last line
  is the answer to "why is this even being built".

A node that is both a root and, from its dependency's point of view, a consumer gets both lines.
That's not a duplicate.

## Resolution

### No provider

```
missing/server.go:11:6: servo: no provider for missing.Store
  needed by *missing.Server  missing/server.go:11:6
  root                       missing/spec.go:9:3
```

Nothing in the graph can produce that type: servo found **zero** possibilities — not several, which
is an [ambiguity](#ambiguous-interface) and headed as one.

The message ends with the **near misses**, when there are any — what came close, and why it does
not count:

```
app/app.go:17:6: servo: no provider for *app.Client
  needed by *app.Server  app/app.go:17:6
  root                   cmd/app/spec.go:12:3

  rejected: app.NewClient (app/app.go:7:6) — variadic parameter — unsupported
  near miss: app.NewConfig (app/app.go:10:6) — returns app.Config, not *app.Config — a type and a pointer to it are two different nodes
```

`rejected:` is a main-module function the scan refused that would have provided the type, with the
reason ([every reason, with its fix](resolution.md#rejection-reasons)); `unusable:` a candidate the
injector [cannot refer to](resolution.md#the-injector-has-to-be-able-to-refer-to-it); `near miss:`
the type's `T`/`*T` twin, or a value whose pointer-receiver methods are what the interface needed;
`note:` a type no function can ever provide. [Near misses](resolution.md#near-misses) has the full
list.

With none of those, in order of likelihood:

1. **The constructor is in a `_test.go` file.** Test files are not scanned at all.
2. **It's an interface with no implementation in the main module.** The structural search covers your
   own module only — and under the default [`--scope`](cli.md#--scope), not a module a `go.work`
   lists beside it.
3. **An explicit `Bind` names a type nothing produces.** Then the reported type is the *bound* type.
   No alternative types are suggested — servo won't second-guess an explicit instruction — but the
   near misses for the bound type are.
4. **Nothing was written yet.** `servo list --rejected` shows everything servo refused, grouped, with
   the type each would have provided.

### No usable provider

```
comp/comp.go:11:6: servo: no usable provider for *comp.pool
  needed by *comp.Service  comp/comp.go:11:6
  root                     cmd/app/spec.go:12:3

  unusable: comp.NewPool (comp/comp.go:6:6) — its result type *comp.pool is unexported, so the App generated in example.com/app/cmd/app cannot name it — export the type, or return an exported interface it implements
```

Something does produce the type, but generated code in this injector's package cannot refer to it:
its result type is unexported, it (or a type it returns) is under another tree's `internal/`, or its
package imports the injector's own package, which would make importing it back a cycle. The
`unusable:` line says which, and what to do. Each of these used to be selected and end in a compile
error inside the generated file that `servo generate` blamed on itself. The same candidates are left
out of [structural search](resolution.md#selection-precedence) — so an adapter that imports your
core package no longer makes an ambiguity of the implementation you can use — and listed by
`servo list --rejected` under `candidates <injector> cannot use:`.

### Ambiguous interface

```
ambiguous/store.go:23:6: servo: ambiguous provider for ambiguous.Store
  needed by *ambiguous.Server  ambiguous/store.go:23:6
  root                         ambiguous/spec.go:9:3

  2 types in this module implement Store — pick one with servo.Bind:
      servo.Bind[Store, *Postgres]()  ambiguous/store.go:11:6
      servo.Bind[Store, *Redis]()     ambiguous/store.go:17:6
```

Several types implement the interface, so auto-binding can't pick one. **The fix is printed
verbatim**: copy one of those lines into your `servo.Build(...)` call. The types are written the way
your spec file writes them — by the name it imports each package under, bare for the spec's own
package (this fixture's spec lives in package `ambiguous`) — and when a line needs an import the
spec file lacks, the message ends with it:

```
  2 types in this module implement store.Store — pick one with servo.Bind:
      servo.Bind[store.Store, *memory.Store]()      memory/memory.go:5:6
      servo.Bind[store.Store, *redisstore.Store]()  redisstore/redisstore.go:5:6
  with these imports in the spec file:
      "example.com/app/memory"
      "example.com/app/redisstore"
      "example.com/app/store"
```

A candidate the injector [cannot refer to](#no-usable-provider) is never one of these: it is left
out of the search rather than counted as a rival.

Candidates are sorted by source position, so the list is stable across runs. If a listed candidate
surprises you — a fake, a second implementation you forgot about — that's information: it is a real
type in your module that really does implement the interface.

### Ambiguous concrete type

```
servo: ambiguous provider for *sqsaccounts.Client
  2 functions produce *sqsaccounts.Client, and servo tells values apart by type alone, so they
  are one contested node, not 2 instances:
      processor.NewClientA  processor/naive.go:5:6
      processor.NewClientB  processor/naive.go:6:6

  Delete all but one. If you want 2 instances, give each its own named type and
  have each constructor return its own — renaming a function cannot change the
  type it returns:

	type PrimaryClient struct{ *sqsaccounts.Client }
```

Two functions in your module return the *same* concrete type, with no interface involved. **No
`Bind` can fix this** — there's nothing to disambiguate between, because identity in the graph is
purely by type, and two functions returning one type are not two instances but one contested node.
Renaming one changes nothing for the same reason.

The fix is to make them distinct types:

```go
type OrdersAccount struct{ *Client }
type AuditAccount  struct{ *Client }
```

Embedding keeps every method available with no delegation boilerplate. A type Go cannot embed —
an unnamed interface, a pointer to an interface, a named pointer type — is suggested as a field
instead, named the way an embedded one would be:
`type PrimaryValue struct{ Value interface{Get() string} }`. The README's
[Multiple instances of the same type](https://github.com/okian/servo/blob/master/README.md#multiple-instances-of-the-same-type)
walks through a complete worked example, and
[Limitations](../limitations.md#there-are-no-tags-or-names-so-you-cant-have-two-of-the-same-type)
explains why there are no tags to solve it with.

**Constructors you don't own.** Ask for a stdlib or third-party concrete type and that package's
own constructors are candidates too — but your module's come first: one `NewDB() *sql.DB` of your
own is selected over `sql.Open` and `sql.OpenDB`, and a lone outside constructor is selected when
you have none. Only several outside ones and none of yours is an ambiguity, and it says what you can
do about functions you cannot delete:

```
servo: ambiguous provider for *database/sql.DB
  2 functions outside this module produce *sql.DB, and servo will not pick one of
  them for you:
      sql.OpenDB  database/sql/sql.go:837:6
      sql.Open    database/sql/sql.go:868:6

  Write its provider in this module — a constructor of your own is preferred
  over all of these:

	func NewDB(...) *sql.DB

  or declare servo.Value[*sql.DB]() and hand one to NewWith.
```

Wrapping the foreign type in one of your own (`servo new adapter postgres` scaffolds one) is often
the better constructor to write. For an interface, your module's implementations are searched
before this is reported. A `context.Context` parameter never gets here: it is the
[construction context](resolution.md#selection-precedence).

### Dependency cycle

```
cycle/graph.go:8:6: servo: dependency cycle detected
  *cycle.A cycle/graph.go:8:6
  *cycle.B cycle/graph.go:12:6
  *cycle.A cycle/graph.go:8:6  (cycle closes here, back to the first line)
```

The full loop is printed, closing on the type it started from. Constructor injection cannot express a
cycle: `A` needs a finished `B`, `B` needs a finished `A`, and there's no ordering that satisfies
both. There is no lazy-injection escape hatch by design.

Break it by extracting what they share into a third type both depend on, or by having one side accept
a callback the other registers after construction.

### Unused supplied value

```
cmd/api/spec.go:13:3: servo: servo.Value[example.com/app/conf.Flags]() is declared, but nothing in
the graph depends on example.com/app/conf.Flags

  A declared value becomes a field on the generated Values struct, so this
  one would be supplied by every caller and read by nobody.

  Two ways out:
    - take it as a constructor parameter somewhere the roots reach:
      func New(v example.com/app/conf.Flags) *Thing
    - delete the servo.Value declaration
```

A [`servo.Value`](spec.md#value) declares a parameter every caller of `NewWith` has to fill in, so
one nothing reads is a cost with no return. Reported against the marker's own call site rather than
against the type, since the declaration is what has to change.

Checked against the production graph only. If a [`servo.Override`](spec.md#override) swaps out the
only consumer of a supplied value, the **test variant** leaves the value off `TestValues` rather than
reporting it: a mock that takes fewer things than the real implementation is the ordinary case, not a
mistake in the spec file.

### Construction context where there is none

```
chat/room.go:20:6: servo: *chat.Room is scoped, but its constructor chat.NewRoom takes a context.Context
```

A constructor's `context.Context` is the ctx `New` was called with, so only constructors `New` calls
can take one. A scoped instance is built inside `Acquire`, after `New` returned — do the work in
`Init(ctx context.Context) error` instead. The same refusal, worded for each, meets a second
`context.Context` among a `ScopeKey` extractor's parameters or a `//servo:` handler's: the first
parameter is already the context that call runs under.

### Notes

```
comp/comp.go:16:18: servo: note: *comp.Consumer's Run method has the signature Run() error, not Run(ctx context.Context) error, so servo will never call it — the Runner capability is detected by signature alone
comp/comp.go:7:6: servo: note: *comp.Pool has a Close method but no Stop(ctx context.Context) error, so servo will not release it at shutdown — add a Stop that calls Close, or have comp.NewPool return a cleanup func: (T, func(), error)
```

Not errors: generation goes ahead. A [capability](lifecycle.md#the-seven-capabilities) is found by
signature alone, so a method with a capability's name and any other signature — `Run()` without a
context, `Stop(ctx)` without an error, a pointer-receiver method on a node built as a value — is
simply never called, and a component with a `Close` or `Shutdown` but no `Stop` is never released.
Each used to leave the node at `capabilities: none` with nothing said. Notes cover the nodes your
own module constructs, and the production graph only.

## Spec file

Full explanations on [Spec file and markers](spec.md#errors-from-this-stage). In short:

| Message | Fix |
| --- | --- |
| `servo: no servo.Build(...) call found` | Run `servo init` |
| `servo: no servo.Build(...) call could be read — a spec file does not type-check` | The `Build` call itself does not resolve (`servo.Buidl`, an import that fails); the type checker's errors are listed beneath |
| `the spec file does not type-check, so … cannot be read` | A marker with no type information, and the type checker's own errors in the spec file listed beneath it — a mistyped type name (often a rename `gopls` made without seeing the tagged file), a package the spec does not import. Nothing else compiles a spec file, so this is where they surface |
| `servo.Bind[I, C](): C does not implement I (missing method M)` | The concrete type cannot stand in for the interface — also `method M has a pointer receiver, so only *C does` and `wrong type for method M`. Checked for `Override` too. A generated mock is the usual cause: it predates the interface's last change, so regenerate it, then run `servo generate` again |
| `servo.X is a marker … knows, but …, which the module requires, does not declare it` / `… declares and … does not know` | The CLI and the runtime are different releases — see [A marker the CLI and the runtime disagree about](#a-marker-the-cli-and-the-runtime-disagree-about) |
| `multiple servo.Build(...) calls found in the same package` | Delete one; a package owns one generated file |
| ``spec file is missing a `//go:build servoinject` constraint`` | Add the tag |
| `servo.Build argument is not a marker call` | Only marker calls belong in `Build` |
| `servo.Build argument must be a Root/Bind/Override/Scoped call with explicit type arguments` | Write markers inline, not via a variable or a helper — or use [`servo.Include`](spec.md#include) for a shared set |
| `unrecognized servo marker "X" inside Build(...)` | Not a marker function |
| `servo.Root expects exactly one type argument` | `Root[T]()` |
| `servo.Bind/Override expects exactly two type arguments` | `Bind[I, C]()` |
| `second type argument must be a concrete type, not an interface` | Name the implementation, not another interface |
| `servo.Bind[…] declared twice` | One bind per interface — unless the first came from an `Include`, in which case the local one wins silently |
| `servo.Value expects exactly one type argument` | `Value[T]()` |
| `servo.Value[…]() declared twice` | One `Value` per type |
| `servo.Include takes exactly one argument, the name of a func() []servo.Marker` | `Include(pkg.Shared)` |
| `servo.Include's argument must name a declared func() []servo.Marker` | Pass the function's name, not a literal, a method value, or a call |
| `servo.Include names …, which is declared in …, a file this build leaves out` | The marker set's file carries a constraint this run doesn't satisfy, or a `GOOS`/`GOARCH` in its name: run with the tags that select it, or gate it on `servoinject` alone |
| `servo.Include names …, which is not a marker set servo can read` | The function is not declared in your module or one it requires — the standard library's, say — so there is no body for servo to read; name a `func() []servo.Marker` of your own |
| ``servo.Include names …, which is declared in a file without a `//go:build servoinject` constraint`` | Tag the shared marker set's file, exactly like a spec file |
| ``X must be exactly `return []servo.Marker{ ... }` `` | The included body is read as syntax, so it can't be a variable, a conditional, or an `append` |
| `servo.Include cycle — X includes itself, through:` | Two shared sets include each other; the path that closed the loop is printed |
| `servo.ConfigFile takes exactly one argument, a string literal path` | `ConfigFile("config.yaml")` |
| `servo.ConfigFile's argument must be a string literal` | The path is read as syntax; a variable or a call can't be |
| `servo.ConfigFile(…): the extension must be .json, .yaml, .yml, or .toml` | It picks the decoder the generated code carries |
| `servo.ConfigFile(...) declared twice` | One file per injector; a local declaration may still replace an included one |

## Configuration

The [`//servo:config`](config.md) directive adds two families of diagnostics. Scanning ones —
malformed directive or tag, unsupported field type, a bad `default=`, two directives in one
package — are module-wide errors reported at the declaration, whether or not any graph uses the
type: the author wrote the directive, and servo never silently half-honors one. Every malformed
declaration is reported, in source order, not just the first one found. A misspelled directive, or
`//servo:config` anywhere but a type's doc comment, is reported with the other
[directive errors](#http-routes) by the one pass that reads every `//servo:` line. Resolution ones —
a hand-written constructor for a config type, two used configs claiming one environment variable
or file key, a scoped constructor depending on a config, a `ConfigFile` nothing reads, and a config
declared in a package the injector cannot import (`config type … cannot be loaded by the injector
in …`, since `New` calls its loader) — are per-graph. The full table, with causes and fixes, is on
[Generated configuration](config.md#diagnostics).

## Loading

### Build errors outside the injector

```
servo: module has build errors:
<the go/packages errors>
```

Some package other than an injector's own doesn't type-check. Servo can't resolve a graph it can't
type-check, so fix the compile error first.

Errors *inside* an injector's own package are deliberately tolerated. On a fresh checkout, before
any generation has run, `main.go` legitimately references a `New` that doesn't exist yet — that's a
reason to generate, not a reason to refuse. That holds for every injector in the module, not only
the ones `--dir` selects, since the whole module is loaded either way.

It holds one step further out, too. An injector in an importable package — `wiring`, whose `New`
a `cmd/app/main.go` calls — is loaded with the `servoinject` tag, which hides its generated file, so
`cmd/app` cannot type-check during a servo run even once `go build` passes. Its `undefined:
wiring.New` and `undefined: wiring.App` are tolerated as well: an error naming a member of an
injector package, at the selector that names it. Anything else wrong in that package is still
reported.

### A directory servo cannot load from

```
servo: directory cmd/ap does not exist — check --dir
servo: . is not inside a Go module (go: pattern ./...: directory prefix . does not contain main
module or its selected dependencies) — create one with `go mod init <module path>`, then add servo
to it with `go get -tool github.com/okian/servo/v3/cmd/servo`
servo: . is in a go.work workspace but inside none of the modules it lists (go: pattern ./...:
directory prefix . does not contain modules listed in go.work or their selected dependencies) — run
servo from one of those modules, or point --dir at one; --scope=workspace lets that run see the
others
servo: no Go package in docs or below is visible to this build configuration (the default one, with
no build tags), so there is no injector to process — run `servo init` there to scaffold one, or
point --dir at an injector's directory
```

The go command could not load anything from `--dir`, and these are its reasons, verbatim in the
parenthesis, with the step that gets past each. A `--dir` that does not exist used to surface as
`fork/exec /usr/local/go/bin/go: no such file or directory`, naming the go binary rather than the
directory; the other three reached you as a missing servo runtime and the advice to run
`servo init`, which none of them needs. At a `go.work` root that is not itself a module, run servo
from inside a module — `--scope` decides whether the others are part of its graph.

### The module does not require servo yet

```
servo: cmd/app/spec.go:5:8: the module does not require github.com/okian/servo/v3 yet (go: no
required module provides package github.com/okian/servo/v3/servo; to add it: go get
github.com/okian/servo/v3/servo) — add it with `go get -tool github.com/okian/servo/v3/cmd/servo`,
which also records the generator in go.mod, so `go tool servo` runs the version the module builds
against
```

A spec file imports the runtime package, and nothing in `go.mod` provides it — usually `servo init`
run from a globally installed binary before the module ever required servo. It used to fail later,
saying the runtime declared none of the capability interfaces. `go get -tool` adds both the
requirement and the `tool` line `go tool servo` needs. A `go.sum` with no entry for servo is named
the same way, with `go mod tidy` as the fix.

### No injector under `--dir`

```
servo: no servo.Build(...) call in cmd/app/handlers or below — the module's injectors are elsewhere;
point --dir at one of them, or at the module root for all of them:
  cmd/app/spec.go:12:2
```

`--dir` [selects which injectors run](cli.md#--dir-and-injector-scope), and it selected none: the
module has injectors, and none of them is in that directory or below it — typically a `--dir`
pointing at a package that *calls* an injector. The ones it has are listed.

### The standard library's export data cannot be read

```
servo: note: golang.org/x/tools v0.49.0 cannot read the export data go1.27.2 writes, so the
standard library was type-checked from source instead, which is slower — with `go tool servo`, run
`go get golang.org/x/tools@latest` in your module; with an installed servo, install a newer one
```

A note on stderr, not a failure. Servo reads the standard library from the compiler's export data,
which takes a load from about two seconds to a quarter of one — and couples servo to a
`golang.org/x/tools` that understands the format of the Go release in use. A Go release that moves
the format leaves an older x/tools unable to read it, which used to fail every command with
x/tools' own `internal error in importing "context" (… export data version 5 is greater than
maximum supported version 4); please report an issue`, naming a different package on each run.
Now the standard library is type-checked from source instead, the way servo loaded everything
before it read export data, and the run carries on, slower, saying so.

The note names both versions and both fixes. `go tool servo` is built against your module's own
build list, so `go get golang.org/x/tools@latest` there puts a newer reader into servo with no servo
release involved; an installed `servo` binary carries the x/tools its release pinned, and only a
newer servo moves it. Should type-checking from source fail as well — a `go/types` too old for the
standard library it is reading — the same sentence is an error, with that failure appended.

### The go command cannot build export data

```
load: go list -export failed: build cache is disabled by GOCACHE=off, but required as of Go 1.12
```

Export data is something the go command builds, and that build can fail before it lists a single
package: the build cache turned off with `GOCACHE=off`, a `-toolexec` in `GOFLAGS` that fails.
The go command's own reason follows the colon; fix what it names. This used to arrive as an empty
load, reported as the [missing runtime package](#missing-runtime-package) below, with the advice
to run `servo init`. The compile check `generate` and `check` run over the generated code asks the
same question, so the same failure there is `verify: go list -export failed: …`; it used to pass,
having compiled nothing.

### Missing runtime package

```
load: servo runtime package github.com/okian/servo/v3/servo is not imported by any file this
build configuration (the default one, with no build tags) can see — either no spec file exists
yet (run `servo init`), or every spec file is gated on a build constraint this configuration
does not satisfy, which makes it invisible to this run
```

Nothing this run can see imports the servo runtime, which for a module with an injector is only
possible if the spec file was excluded. The two causes are named in the message because they need
different fixes, and the parenthesis tells you which configuration you were in:

- **No spec file yet.** Run `servo init`.
- **Every spec file is gated out.** With [variants](cli.md#build-variants), a spec constrained
  `//go:build servoinject && prod` is invisible to a plain `servo check`, and one constrained
  `servoinject && !prod` is invisible to `servo check --tags=prod`. The configuration is named
  verbatim — `-tags=prod`, or `the default one, with no build tags` — so the fix is to add or drop
  the flags that spec is written for.

### A runtime older than the CLI

```
servo: warning: github.com/okian/servo/v3/servo v3.5.0 declares no Readier, a capability servo v3.6.0
detects — the module requires an older servo than the CLI it is generated with, so generated code
calls no such method until the two match: run the CLI the module pins (`go tool servo`), or update
the module (`go get github.com/okian/servo/v3@latest`)
```

A warning, on one line, not an error. The capability interfaces come from the servo package your
`go.mod` pins, while the list servo looks for is the CLI's, so a CLI that knows a capability added
after that release finds no declaration for it. That capability is skipped — no node is reported as
having it, and nothing generated calls the method — and every command carries on, where it used to
fail outright with `capability interface Readier not found`. A runtime declaring *none* of them is
still an error: every one has existed since the first release, so that package did not load —
check that `go.mod` requires `github.com/okian/servo/v3` and that `go mod tidy` has filled `go.sum`.

### A marker the CLI and the runtime disagree about

```
cmd/app/spec.go:14:3: servo.Trace is a marker servo v3.5.0 knows, but
github.com/okian/servo/v3/servo v3.3.0, which the module requires, does not declare it — the module
requires an older servo than the CLI generating it: update the module (`go get
github.com/okian/servo/v3@latest`) to use servo.Trace, or keep its release and generate with the
CLI it pins (`go tool servo`)

cmd/app/spec.go:14:3: servo.Decorate is a marker github.com/okian/servo/v3/servo v3.7.0, which the
module requires, declares and servo v3.5.0 does not know — the module requires a newer servo than
the CLI generating it: regenerate with the CLI the module pins (`go tool servo generate`), or update
this one
```

The marker side of the same skew. The spec file is type-checked against the runtime your `go.mod`
resolves, while the markers servo reads are the CLI's: a runtime older than the CLI cannot declare a
marker the spec uses, and a runtime newer than the CLI can declare one this CLI has never heard of.
Neither is a mistake in the spec, and the second used to read as one — `unrecognized servo marker
"Decorate"` — with nothing naming the two versions. Either version is left out when there is none
to name: a development build of the CLI, a runtime replaced by a directory. Options are covered
the same way: an unknown `servo.HTTP`, `servo.Use`, `servo.Trace` or `servo.Scoped` option the
runtime declares gets this message, not "is not an option".

### Multiple injectors in scope

```
servo: multiple injectors found in this scope — pass --dir to pick one:
  cmd/basic/spec.go:16:2
  cmd/migrator/spec.go:11:2
```

From `graph`, `explain`, `why`, `list` or `config` — commands that answer a question about *one*
graph. Point `--dir` at one injector's own directory. `generate`, `check` and `doctor` process all of
them and never print this.

## Scopes

These four are what a hand-written registry beside servo cannot give you. All are `generate`
failures, and all four carry the same needed-by chain a resolution diagnostic does. The
extractor-cycle chain leads to the scoped dependency rather than to the extractor, whose own
position is printed above it; the undeclared-scope chain comes from the traversal path that reached
the type, because that check fires while candidates are still being selected. See [Scoped instances](scopes.md) for the feature they belong to.

They are the four *named* scope diagnostics. Thirty-seven narrower ones — a stray scope key, a node
two scopes both claim, a scoped type declared as a root, a bound accessor — are tabulated under
[Other scope errors](#other-scope-errors) below.

### Widening

```
widening/rooms.go:37:6: servo: *widening.Room is scoped, but *widening.Server is a
singleton that depends on it
  needed by *widening.Server  widening/rooms.go:37:6
  root                        widening/spec.go:9:3

  A singleton is constructed once and held for the life of the process, so it
  would capture whichever *widening.Room happened to be built first and hand that same
  one to every caller afterwards, whatever key they present. Nothing about the
  running program would say so.

  Two ways out:
    - depend on the accessor instead: change widening.NewServer's parameter from *widening.Room to widening.Rooms,
      and call Acquire(ctx) per request
    - make *widening.Server scoped too, by giving it a dependency on widening.RoomKey
```

The one this feature exists for. A singleton holding a scoped instance pins one key's instance for
the life of the process — the first room anyone joins becomes everyone's room — and it is invisible
until production.

The usual fix is the first one: take the accessor interface and `Acquire(ctx)` inside the method
that needs it, so the reference lasts for one call rather than for the process.

### Cross-scope

```
crossscope/nested.go:50:6: servo: *crossscope.Room and *crossscope.Tenant are in
different scopes
  needed by *crossscope.Room  crossscope/nested.go:50:6
  root                        crossscope/spec.go:10:3

  *crossscope.Room is keyed by crossscope.RoomKey
  *crossscope.Tenant is keyed by crossscope.TenantKey

  Nested scopes are deliberately not supported in this release: one instance
  per key pair means two reference counts and two linger windows with no single
  owner, and no obvious answer for what happens when the outer one evicts while
  the inner one is still held. This is a rejection, not an oversight.

  Depend on *crossscope.Tenant's accessor interface instead and Acquire it inside the method
  that needs it, so the inner instance is held only for that call.
```

Nested scopes are rejected on purpose, not as a side effect of how the reachability pass happens to
work. Holding the inner scope's *accessor* is fine and is the intended answer — that edge crosses
no scope boundary, because an accessor is not an instance.

### Extractor cycle

```
extractor/session.go:36:17: servo: *extractor.Session's ScopeKey extractor depends on
*extractor.Decoder, which is itself scoped
  ScopeKey                      extractor/session.go:36:17
  needed by *extractor.Decoder  extractor/session.go:25:6
  needed by *extractor.Session  extractor/session.go:32:6
  root                          extractor/spec.go:10:3

  The extractor is what decides which instance a caller gets, so it runs before
  any instance exists. Everything it takes must already be constructed — that is,
  a singleton.
```

A `ScopeKey` method may take dependencies after its `ctx`, and they resolve as ordinary graph
edges. They just cannot be scoped: choosing the instance is the thing the extractor is being called
to do.

### Undeclared scope

```
undeclared/tenant.go:22:16: servo: *undeclared.Tenant declares a ScopeKey method but no
servo.Scoped declares it
  ScopeKey                      undeclared/tenant.go:22:16
  needed by *undeclared.Tenant  undeclared/tenant.go:20:6
  needed by *undeclared.Server  undeclared/tenant.go:32:6
  root                          undeclared/spec.go:9:3

  A ScopeKey method is what makes a type keyed rather than a singleton, and servo
  will not infer the rest of the declaration from it: the accessor interface has
  to be one you name, because servo cannot emit a type into your package.

  In package undeclared:

	type Tenants interface {
	    Acquire(ctx context.Context) (*Tenant, func(), error)
	}

  In servo.Build:

	servo.Scoped[*undeclared.Tenant, undeclared.Tenants](),

  Or delete the ScopeKey method, if this type is meant to be an ordinary singleton.
```

The mirror image also fires — a `servo.Scoped[T, I]` whose `T` has no `ScopeKey` method names the
missing method and prints its required shape.

### Other scope errors

Resolution stage — these carry the `servo: ` prefix:

| Message | Cause |
| --- | --- |
| `servo: X.ScopeKey must not name its receiver` | Generated code calls it on a typed nil. Write `func (*T) ScopeKey(...)` |
| `servo: ScopeKey's first parameter must be context.Context` | The key comes from the request context |
| `servo: ScopeKey must return exactly (K, error)` | Without the error, a missing key becomes the zero `K` |
| `servo: ScopeKey must not be variadic` | Every parameter after `ctx` is a dependency, and a variadic one is a slice |
| `servo: ScopeKey's key type is X, which is not a defined type` | Scope identity is type identity; `string` cannot be one |
| `servo: ScopeKey's key type X is an interface` | Two callers' dynamic types would never compare equal |
| `servo: ScopeKey's key type X is not comparable` | It keys the instance map |
| `servo: ScopeKey must be declared on the pointer receiver` | The node in the graph is `*T`, and a value receiver would dereference the typed nil |
| `servo: X is a scope key and is not resolvable outside its scope` | A singleton asked for the key type directly |
| `servo: X depends on K, which is a scope key` | A singleton took a scope's key — usually a node reached only through a *different* scope's sub-graph |
| `servo: X is keyed by K1 but depends on K2, another scope's key` | The same, for a node that is in a scope, just not that one |
| `servo: X belongs to two scopes at once` | Two scopes both claim one node. A nested scope by another route |
| `servo: servo.Root[X] declares a scoped type as a root` | A root is held by the App for the life of the process — widening, with the App as the consumer |
| `servo: servo.Root[I] declares a scope accessor as a root` | An accessor is generated code, not a node a root can pull in |
| `servo: I is a scope accessor interface and cannot be bound or overridden` | servo emits the value satisfying `I`; there is no selection for a `Bind` to change |
| `servo: F produces I, which is a scope accessor interface` | The same mistake made with a constructor: an accepted candidate resolution would never select |
| `servo: T's ScopeKey extractor takes I, its own scope's accessor` | `Acquire` calls the extractor, so acquiring from inside it recurses without bound. *Another* scope's accessor is fine |
| `servo: scope accessor interface I cannot be satisfied` | `I` declares a method the generated accessor does not have, or one whose signature does not match |
| `servo: conflicting servo.Linger for scope K` | Two declarations share a key type — and therefore one registry — but disagree about its window |
| `servo: conflicting servo.Max for scope K` | The same, for the instance cap |
| `servo: X is scoped, but its constructor F takes a context.Context` | A constructor's `context.Context` is `New`'s ctx, and a scoped instance is built inside `Acquire`, after `New` returned. Take the context in `Init(ctx)` |
| `servo: X's ScopeKey extractor takes a second context.Context` | Its first parameter is the acquiring context; later parameters are singletons, resolved once |

Spec-parsing stage — read as syntax, before resolution, and reported without the prefix:

| Message | Cause |
| --- | --- |
| `servo.Scoped expects exactly two type arguments` | Wrong arity |
| `servo.Scoped's first type argument must be the concrete scoped type, not an interface (X)` | An interface where the scoped type belongs |
| `servo.Scoped's first type argument must be a pointer, not X` | `Acquire` reports failure by returning a nil instance beside the error, and a value type has no nil to return |
| `servo.Scoped's second type argument must be an interface` | A concrete type where the accessor interface belongs |
| `servo.Scoped's accessor interface I declares no methods` | `any` is satisfied by everything |
| `servo.Scoped[T, ...] declared twice` | One scoped type, one declaration |
| `servo.Scoped[..., I] declared twice` | One accessor interface cannot stand for two scoped types |
| `servo.Scoped's arguments must be servo.Linger(...) or servo.Max(...) calls` | Something else in the option list |
| `servo.X is not a scope option` | A `servo` marker that is neither `Linger` nor `Max` |
| `servo.Linger is a scope option, not a Build marker` | An option at the top level of `Build` |
| `servo.Linger/Max declared twice in the same servo.Scoped` | One value each per declaration |
| `servo.Linger/Max expects exactly one argument` | Wrong arity |
| `servo.Linger's argument must be a constant expression` | The spec file is read as syntax, never executed |
| `servo.Linger's argument must be a constant time.Duration` | The folded constant is not an integer |
| `servo.Max's argument must be a constant integer` | The same, for `Max` |
| `servo.Linger(...) must not be negative` | Use `servo.Linger(0)` for die-with-the-last-holder |
| `servo.Max(N) must be positive` | A scope that can hold no instances can never hand one out |

## HTTP routes

The `//servo:` comment prefix is reserved in full, so nothing under it can silently no-op: a typo'd
method is an error, not an unserved route. One pass reads every `//servo:` line in the main module
on every load, whether or not any spec declares `servo.HTTP()`: each is a route on a function, a
`//servo:config` on a type, or one of the errors below — none can fall between the route and config
scanners, as `//servo:confg` on a type and `//servo:config` on a `var` once did. The line is split
on any whitespace, so a tab between tokens, or a space after the colon, means the same to `servo
generate` as to [`servo-vet`](#servo-vet), which reads lines with the same code.

The directive itself:

| Message | Cause |
| --- | --- |
| `unknown //servo: directive "pots" — the prefix is reserved for //servo:config on a type declaration and the route verbs (…) on a function` | Neither a route verb nor `config` |
| `empty //servo: directive` | A bare `//servo:` |
| `//servo:post must be the doc comment of a top-level function — anywhere else it registers no route` | A route verb on a `var`, a type, inside a function body, or floating |
| `//servo:config must be the doc comment of a type declaration — anywhere else no loader is generated` | `//servo:config` on a function, a `var`, a field |
| `//servo:config documents a group of type declarations, not one type` | On a parenthesized `type ( … )` group: move it onto the type it configures |
| `//servo:post needs a route pattern` | No pattern after the method |
| `route pattern "order" must start with "/"` | Patterns are rooted |
| `//servo:post takes exactly one pattern` | Trailing text after the pattern |
| `route GET /a/{x}suffix cannot be registered on net/http.ServeMux: …` | net/http's own words, for a malformed pattern — the same registration that would panic at startup, run at generate time |
| `//servo: route directives need a servo version that exports the servo.Response family` | The CLI is newer than the module's `servo/v3`; reported once per file |

Several problems on one directive line are reported in the order the pattern names its segments,
and ties at one position are broken by the message, so two runs print the same report.

The handler it documents:

| Message | Cause |
| --- | --- |
| `handlers must be top-level functions, not methods` | Directive on a method |
| `handler app.order must be exported` | The generated server has to call it from the injector package |
| `handler app.Order must not be generic` / `must not be variadic` | Same rules as providers |
| `first parameter must be context.Context` | The request's context is the contract |
| `must return (R, error) where R is a servo response type (servo.Json[T], servo.Xml[T] or servo.Response)` | Any other result shape, including a concrete error type |
| `route GET /ping is declared in package main (…), which no other package can import, and that package has no servo.Build(...) to serve it` | A handler beside a `main.go` with no injector: only an injector in that same package can call it, and every other injector skips it |
| `… and that package's injector declares no servo.HTTP()` | The same, with an injector there that serves nothing |

The request struct and its pattern:

| Message | Cause |
| --- | --- |
| `request struct type app.orderReq must be exported` | The generated server constructs one |
| `field X … must be a scalar (string, bool, int/uint families, float32/64)` | A bound field the generated `strconv` parsing can't decode |
| `field X … has more than one binding tag` | A field binds from exactly one source |
| `field x … is unexported but carries a binding tag` | The decoder cannot assign it |
| `fields Page and P … both bind query "page"` | Last-write-wins would be silent |
| `request struct … mixes form fields with JSON body fields` | A body is a form or JSON, not both |
| `request struct … declares JSON body fields, but GET requests carry no body` | Body/form fields on GET/HEAD/DELETE/OPTIONS |
| `field Category … binds path "category", but the pattern "/order" has no {category} segment` | Tag without a segment reads empty forever |
| `pattern segment {category} … has no path:"category" field` | Routing data the handler never sees |

Groups, middleware and extractors:

| Message | Cause |
| --- | --- |
| `group "grp" must match [A-Za-z0-9_-]+` | A directive's trailing token failing the group grammar |
| `//servo:post takes a pattern and an optional group — unexpected …` | A fourth directive token |
| `group "backoffice" is not declared by any injector` | A group token no spec's `servo.Group` declares — module-wide, so a typo cannot silently strand a route |
| `T must have a method Middleware(next http.Handler) http.Handler` | A `servo.Use` type without the wrap method (or the wrong shape) |
| `servo.Use[T] selects group "x", but this injector serves only: …` | A `Group` selector naming an unserved group |
| `servo.Use[T]'s selector "POST /p" matches no served route` | A `Route` selector matching nothing |
| `T must have a method Extract(r *http.Request) (T, error)` | A `servo.Extract` type without the extractor method (or the wrong shape) |
| `two extractors produce *auth.User` | A produced type maps to exactly one extractor |
| `*auth.User has both a provider (…) and an extractor (…) — … never both` | A type cannot be both constructed once and extracted per request |
| `T is scoped, but servo.Use wraps a singleton server` / `servo.Extract needs a singleton` | Middleware and extractors are resolved once |
| `http: group "telemetry" declared in the spec but missing from HTTPConfig.Groups` | Runtime (`New`) — group names are generate-time facts, port numbers are runtime values |

The route set and the graph:

| Message | Cause |
| --- | --- |
| `duplicate route POST /order — first declared at …` | Exact group+method+pattern collision, both positions named; the same route on two different groups is two servers and is legal |
| `route … cannot be registered on net/http.ServeMux: … conflicts with pattern …` | A wildcard overlap net/http itself refuses |
| `no provider for *….servo.HTTPConfig` + `needed by HTTP server (servo.HTTP())` | `servo.HTTP()` declared, no config provider |
| `no provider for *app.Missing` + `needed by handler app.Order (POST /order/…)` | An unresolvable handler dependency — with a hint about binding tags when the type looks like a forgotten request struct |
| `*chat.Session is scoped, but handler app.Me (GET /me) receives it from the HTTP server` | The widening rule, with the emitted server as the capturing singleton; depend on the accessor instead |
| `handler api.Update (POST /settings) takes *settings.Settings as its request struct, but the graph provides that type: settings.New constructs it (…)` | The second parameter is decoded from the request whenever it points to a struct with binding or json tags, so a provided type there would be whatever the client sent — `{"admin":true}` included. Give the route a request type of its own and take the provided one after it. Also for a type a `servo.Value`, a scope, an extractor or a `//servo:config` provides |
| `handler app.Order (GET /order) takes a second context.Context` | Later parameters are resolved once, when the server is built, so a second context would be `New`'s, not the request's |

## Tracing

[`servo.Trace`](tracing.md) adds declaration-time checks — the type argument must be a named,
non-empty interface the injector package can implement, and `Expected` must name an exported
package-level error — and three resolution-time ones: a traced interface nothing depends on
(same shape as an [unused supplied value](#unused-supplied-value), checked per generated file), a
module in which nothing imports the OpenTelemetry trace API, and a missing
`trace.TracerProvider` provider, which replaces the generic no-provider message with one that
says the constructor is yours to write:

```
cmd/api/spec.go:21:3: servo: no provider for go.opentelemetry.io/otel/trace.TracerProvider
  needed by the servo.Trace wrapper for example.com/app/store.Store  cmd/api/spec.go:21:3
  needed by *example.com/app/api.Server                               api/api.go:15:6
  root                                                                cmd/api/spec.go:12:3

  Every servo.Trace wrapper takes the tracer provider as an ordinary dependency —
  there is no global fallback. Write a constructor whose result implements it;
  the SDK's *sdktrace.TracerProvider does, and so does a type of your own that
  embeds it and adds the Stop servo will call at shutdown:
  ...
```

A fourth refuses, for this release, a wrapper around a scoped implementation. The full table,
with causes and fixes, is on [Traced interfaces](tracing.md#diagnostics).

## Emission

```
emit: generated source failed to format (this is a servo bug, not a user error):
<the go/format error>
---
<the unformatted source>
```

Servo emitted source that `gofmt` couldn't parse. As the message says, that is a bug in servo, not
in your code. The full unformatted output is included so it can go straight into an
[issue](https://github.com/okian/servo/issues).

## Commands

### `generate`

```
servo: the generated code does not compile — it was written anyway, so it can be inspected:

  cmd/app/servo_gen.go:93:19: cannot use mock (variable of type *mocks.MockStore) as store.Store value in argument to api.New: *mocks.MockStore does not implement store.Store (missing method Count)

The usual cause is in the code it was generated from, not in servo:
  - a servo.Bind or servo.Override type that no longer implements its interface,
    typically a mock not regenerated since the interface changed;
  - a provider the injector's package cannot refer to: one under another tree's
    internal/ directory, one whose package imports the injector's package, or one
    taking or returning another package's unexported type;
  - a declaration in the injector's package with a name the generated code declares too.
Fix the cause, then run `servo generate` again. If none of these explains it, it is a bug in
servo: please report it at https://github.com/okian/servo/issues with the lines above
```

After writing, `generate` compiles every file it wrote under the build configuration the
generated files are gated for. Every error `go build` would stop on inside one of them is reported
with its position and fails the run: the type checker's, and the go command's own — an import of
another tree's `internal/` package, or an import cycle a generated import closes, placed at that
import — which arrive before the type checker and are reported alone, as `go build` reports them.
The file stays on disk so it can be inspected. Errors elsewhere — a `main.go` the generator has
nothing to do with — are not reported here. The causes the message lists are the usual ones, and
they are in the code the file was generated from; regenerate a stale mock first.

```
servo generate: warning: could not compile the generated code in example.com/app/cmd/app: no required module provides package gopkg.in/yaml.v3; to add it: go get gopkg.in/yaml.v3
servo generate: run `go mod tidy`, then `servo generate` again to check that it compiles
```

Not an error, but the generated code in that package was not compiled. Its imports cannot be
resolved yet — typically the first generated file to import a config decoder, before
`go mod tidy` has added it — and every error after a failed import describes the import rather
than the code. Do what it says. `check` prints the same warning.

```
servo generate: removed cmd/app/servo_gen_test.go — the spec no longer declares servo.Override
```

Not an error. The override variant is written only while the spec declares a `servo.Override`;
once the last one goes, nothing would ever regenerate the file, so `generate` removes it and says
so on stderr. Only a file carrying servo's own generated-code header is ever touched.

```
servo generate: removed internal/oldcfg/servo_config_gen.go — its package no longer declares a //servo:config type
```

Not an error. The `//servo:config` type that loader was generated for moved to another package,
or lost its directive; left behind, the loader would still compile into every build and fail it
with `undefined: Config`. Only a file carrying servo's own generated-code header is touched, and
only where nothing could declare the type: a directory with no other non-test Go file, or a
package this run loaded and found no `//servo:config` type in.

```
example.com/app/cmd/app: in the NewTestApp graph (servo.Override at cmd/app/spec.go:14:3): servo: 1 diagnostic(s):
```

The diagnostics that follow are about the graph `NewTestApp` builds, in which each
`servo.Override` replaces its interface's production binding; the production graph resolved.
Nothing is written for that injector until both resolve.

```
servo generate: note: example.com/app/cmd/app declares no servo.Root and no servo.HTTP(), so its App constructs nothing and Run returns at once — add a servo.Root[T]() for each component the program runs
```

Not an error: the empty `servo.Build()` that `servo init` scaffolds. The program it generates
builds, and exits as soon as it starts.

### `check`

```
servo check: cmd/api/servo_gen.go is stale — run `servo generate`
--- cmd/api/servo_gen.go (committed)
+++ cmd/api/servo_gen.go (fresh)
@@ -61,7 +61,7 @@ func New(ctx context.Context) (*App, error) {
 	if err != nil {
 		return nil, err
 	}
-	server := api.New(db)
+	server := api.New(db, cache)
 	a.server = server
 	a.startupReport.Nodes = append(a.startupReport.Nodes, servo.StartupNode{Name: "*example.com/app/api.Server"})
 

note: this is servo v3.5.0. If regenerating does not settle it, the machine that
      committed the file was running a different version — compare `servo version`, and
      pin one for everybody with `go get -tool github.com/okian/servo/v3/cmd/servo`.
```

The committed file doesn't match a fresh generation; the diff has three lines of context and,
after each hunk's line numbers, the declaration the change sits under. Run `servo generate` and
commit the result. The command it names repeats the `--dir`, `--scope` and build flags `check` was
given. A file that differs only in line endings — a Windows checkout with `core.autocrlf` — is
not stale.

The trailing note is on every stale report, because "stale" has one other cause the diff cannot
distinguish: two machines on two servo versions produce a real difference in a file neither of them
edited. If `check` fails in CI but passes locally, that is the usual reason — compare the two
`servo version`s and pin one with `go get -tool`, which is what the `tool` directive in `go.mod` is
for.

```
servo check: cmd/api/servo_gen.go does not exist — run `servo generate`
```

The file was never generated, or was deleted. Same fix, plus commit it — see
[`doctor`](cli.md#doctor).

Both messages cover `servo_gen_test.go` too, when the spec declares a `servo.Override`: the
override variant is compared the same way. And in the other direction:

```
servo check: cmd/api/servo_gen_test.go is left over from a spec that no longer declares
servo.Override — run `servo generate`, which removes it
```

```
servo check: internal/oldcfg/servo_config_gen.go is left over from a //servo:config type its
package no longer declares — run `servo generate`, which removes it
```

The loader of a config type that moved or lost its directive: see the matching `generate` line
above.

```
servo check: the generated code does not compile:
```

Followed by `generate`'s compile report. The committed file matches a fresh generation, and still
does not compile — typically a `servo.Bind` to a mock whose interface has since grown a method,
which generates the same broken file on every run. Fix the cause, then regenerate.

For a [variant](cli.md#build-variants), both messages name the flags that would actually produce
that file — ``run `servo generate --tags=prod` `` — since a bare `servo generate` would regenerate
the default variant and leave the missing one missing.

### `doctor`

```
servo doctor: problems found
```

Printed after the report when any line was `[FAIL]`. Read the report — each line names its own
problem. `[WARN]` and `[INFO]` lines never cause this: `WARN` is a check that could not reach a
verdict (no git, no repo), and `INFO` is the variant inventory, which reports rather than judges.

```
servo doctor:
  [FAIL] servo.prod_gen.go is generated from a spec that no longer exists — delete it, or
         restore the spec file it came from
```

The one `doctor` check nothing else makes. A generated file whose spec was deleted still compiles
into whichever build satisfies its constraint, and nothing will ever regenerate it, so it drifts
from the moment the spec went away.

### `explain` and `why`

| Message | Cause |
| --- | --- |
| `servo: no node matches "X"` | Nothing in the graph matches `X`, exactly or as a suffix. Queries accept a leading `*`, an interface (answered with the node bound to it), and package-qualified generic instances such as `repo.Repo[model.User]`. The message goes on to say what servo knows about a type of that name: a provider no root reaches, an interface nothing asks for, a type only an `Override` puts in `NewTestApp`'s graph, or a function that returns it but was rejected, and the rule that rejected it |
| `servo: "X" matches multiple nodes, be more specific: …` (or `… multiple interfaces …`) | Ambiguous suffix. The candidates are listed; pick one |
| `servo why: X is not reachable from any root` | The node resolved but no root — and no `servo.HTTP()` handler, middleware or extractor, which `why` searches from next — depends on it |
| `usage: servo explain [--dir path] [--json] … <type>` | Zero or several positional arguments. Flags may come before or after the type |
| `usage: servo why [--dir path] [--all] [--json] … <type>` | The same, for `why`; `--all` lists every path from a root, not just the shortest |

### Others

| Message | Cause |
| --- | --- |
| `servo: unknown command "X"` | Typo, or a flag placed before the command name. The full command list is printed underneath it, and again by `servo help` |
| `servo graph: unknown --format "X" (want text\|json\|dot\|mermaid)` | Unsupported format |
| `servo init: <path> already exists` | `init` never overwrites the spec file |
| `servo new: unknown kind "X" (want component\|adapter\|mock-adapter)` | Typo in the scaffold kind |
| `servo new mock-adapter: unknown tool "X" (want moq\|mockery\|gomock)` | Only those three are scaffolded |
| `flag provided but not defined: -X` | Standard `flag` package error — check the flag belongs to that command |

## `servo-vet`

```
spec.go:9:2: servo: servo.Build called in a file without a `//go:build servoinject` constraint —
it will compile into the real binary and panic at runtime; run `servo init` or add the tag
```

```
chat/chat.go:91:6: servo: ScopeKey must not name its receiver — servo calls it on a typed nil,
so a receiver the body can reach is a nil dereference in production; write
`func (*T) ScopeKey(...)`
```

Two of the things [`servo-vet`](cli.md#servo-vet) reports, and two of the mistakes the compiler
cannot catch on its own. The third is a malformed or misplaced `//servo:` comment directive,
reported with the message `generate` prints for it — the analyzer runs the generator's own pass
over every line under the prefix, so the two cannot disagree about one. The generator makes those
three checks too; the analyzer catches them anywhere, in your editor, before generation runs at
all — including in packages no injector has reached yet.

The first fires for any marker — `Build`, or any `servo` function returning `Marker`, `HTTPOption`,
`ScopeOption` or `TraceOption`, which is how a marker added later is covered without a change to
the analyzer — called qualified or, through a dot import, bare. `Include` is the one it earns its
keep on, since a shared marker set lives in its own package, away from any spec file, which is
where the tag is easiest to forget.

Two checks exist only in the analyzer:

```
api/api.go:14:1: servo: this comment reads as //servo:get /spaced, but the space after // makes it
an ordinary comment that `servo generate` never sees — write //servo:get /spaced
```

A near miss: a comment that would be a well-formed directive without the space after `//`. Go reads
it as prose, and so does `generate`, which has no way to tell it from prose without claiming
everything that mentions `servo:` — the analyzer requires the rest of the line to parse, and skips
generated files.

```
comp/comp.go:16:18: servo: Run has the signature Run() error, not Run(ctx context.Context) error, so
servo will never call it — it has the name of servo's Runner capability, which is detected by
signature alone
```

A capability-named method servo will never call, on a type a constructor in the package returns.
`generate` says the same about the nodes of a resolved graph, as a [note](#notes); the analyzer
reaches types no injector has yet. A `Close` with no `Stop` is left to `generate`, which knows the
type really is a graph node.

### `-tags` is refused

```
$ servo-vet -tags=prod ./...
servo-vet: -tags does not work here — it is go/analysis's own no-op flag, so this run would
silently analyse only the default configuration.

To check a tagged configuration, drive servo-vet through the go command, which does understand
build flags:

	go vet -tags=prod -vettool=$(which servo-vet) ./...
```

Exit code **2**, not 1 — this is the tool declining to run, `go vet`'s own convention, and the one
place a servo binary exits with anything other than 0 or 1.

`go/analysis` registers a `-tags` flag on every `singlechecker` binary and documents it as "no
effect (deprecated)": the checker builds its own `packages.Config` with no build flags, so the run
would exit 0 having analysed the default configuration while whoever typed it believes `prod` was
covered. There is no hook to make it work — the config is internal to `x/tools` — so it is refused
with the invocation that does work. The refusal reads `os.Args` directly, so it catches `-tags`,
`--tags`, `-tags=prod` and `-tags prod` anywhere in the argument list.

## Runtime reports are not diagnostics

One distinction worth keeping clear. Everything above happens at build time. At run time, servo
reports rather than diagnoses: `Shutdown` returns a [`servo.Report`](servo-package.md#report), and a
line like

```
*api.Server: abandoned: context deadline exceeded
```

is not a servo error — it's servo telling you a component of yours didn't stop within its budget.
[Lifecycle](lifecycle.md#the-stop-budget) covers what to do about it.
