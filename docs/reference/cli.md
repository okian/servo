# CLI commands

**Who this is for:** anyone running `servo` from a terminal, a `go:generate` directive, or a CI
job, who needs to know exactly what a command does and what it will print.

Every command below was run against
[`examples/basic`](https://github.com/okian/servo/tree/master/examples/basic) to produce the output
shown. Positions are printed as absolute paths, shortened on this page to fit the column — with one
exception: `graph` prints them relative to the module root, because it shares its output struct with
the generated `App.Graph()` and the two have to agree byte for byte.

## Installing

```
go install github.com/okian/servo/v3/cmd/servo@latest
```

That puts a `servo` on your `PATH` pinned to nothing in particular. For a project, add the generator
to the module instead, so its version is recorded in `go.mod` beside every other dependency and
every developer and every CI runner runs the same one:

```
go get -tool github.com/okian/servo/v3/cmd/servo
```

It is then invoked through the go command:

```
go tool servo generate
```

**Not `go run github.com/okian/servo/v3/cmd/servo`.** A consumer module requires servo for the
marker package alone, so the generator's own dependencies are not in that module's build list and
the command fails before it starts:

```
missing go.sum entry for module providing package golang.org/x/tools/go/packages
(imported by github.com/okian/servo/v3/cmd/servo)
```

`go get -tool` adds a `tool` directive to `go.mod` and pulls those dependencies in, which is what
makes `go tool servo` resolve. It is a one-time step per module, and the one
[`servo init`](#init) prints.

## Invocation

```
servo [<command>] [flags] [arguments]
```

**The command is optional.** If the first argument doesn't start with `-`, it is taken as the
command name. Otherwise the command defaults to `generate`, so `servo`, `servo generate`, and
`servo --dir=cmd/api` are all generate invocations. That default is the contract, not an accident:
a bare `servo` generates.

**The default does not swallow `-h` or `--version`.** A leading dash otherwise means "no command",
which for a while made `servo -h` parse as `generate --help` and print `generate`'s four flags — the
one place a new user looks for the other twelve commands — and `servo --version` print `generate`'s
usage twice and exit 1. Both are recognised before the command is extracted: `-h`, `--help`,
`-help` and [`help`](#help) all print the same list, `--version` and `-version` print what
[`version`](#version) prints, and all of them exit 0. (`-v` is not one of them: a bare `servo -v` is
a `generate`.)

**Every command answers `-h`.** `servo <command> -h`, or `--help`, prints that command's usage line,
what it does, and every flag with its default — the same as `servo help <command>` — on stdout,
and exits 0, whatever else is on the line. `servo new component --help` is help, not a component
named `--help`.

**Flags go before or after positional arguments.** `servo explain --json api.Server` and
`servo explain api.Server --json` are the same command, and `--` ends the flags, for an argument
that starts with a dash. The go flag package alone stops at the first non-flag argument, which made
the order the help text itself advertised — `servo explain <type> [--dir]` — a usage error, and
`servo check ./... --tags=prod` a silent check of the default configuration. Both single and double
dashes are accepted (`-dir`, `--dir`), with either a space or an equals sign (`--dir cmd/api`,
`--dir=cmd/api`).

**Arguments a command does not take are ignored, out loud.** `generate`, `check` and the other
commands that read a directory take it from `--dir`, never from an argument. `servo generate
./cmd/app` — what a wire user types — still generates every injector under `--dir` and exits as it
always has, since scripts depend on that, but it now says so on stderr:

```
servo generate: ignoring "./cmd/app" — generate takes no arguments and reads every injector under --dir ("."); to narrow it, pass --dir ./cmd/app
```

**Exit status is 0 or 1.** Success is 0, including a help request. Every failure — a resolution
diagnostic, a stale generated file, an unknown command, a bad flag — is 1. There are no other exit codes, so a CI job needs no
special-casing. (The separate [`servo-vet`](#servo-vet) binary exits 2 when it refuses a flag, which
is `go vet`'s own convention for "the tool would not run", not a servo diagnostic.)

**Diagnostics go to stderr, results to stdout.** `generate` and `check` print nothing on stdout,
and on success nothing at all unless something is worth knowing, which goes to stderr: a leftover
file `generate` removed, a graph with nothing in it, a package whose generated code could not be
compile-checked, an argument the command ignored. `generate -v` lists every generated file there
too — written, unchanged or removed. Without any of those, silence means the work is done.

## `--dir` and injector scope

`--dir` (default `.`) chooses which injectors a command processes: the ones `./...` matches from
it — that directory and every directory below. It chooses nothing else. Servo always loads the
**whole main module** `--dir` is in, so a graph resolved from an injector's own directory is the
graph resolved from the module root: the same implementations for interface search, the same
[`//servo:` routes](http.md), the same [`//servo:config`](config.md) types, the same capability
set.

That matters because the scaffolded [`go:generate` directive](spec.md#the-gogenerate-directive)
runs servo from the injector's own directory. A load narrowed to what that directory imports — what
`--dir` used to mean — could auto-bind the one implementation the injector happened to import where
the module root saw two, or emit an HTTP server serving none of the routes in packages nothing
imported yet, and `servo check` from the root then disagreed with the file `go generate` wrote.

An *injector* is a package containing a `servo.Build(...)` call. A module can hold several — a
monorepo's `cmd/api`, `cmd/worker` and `cmd/migrator` each wiring their own graph — and commands
split into two groups on how they handle that:

| Behaviour | Commands |
| --- | --- |
| Processes **every** injector `--dir` selects, reporting all of them | `generate`, `check`, `doctor` |
| Answers a question about **one** graph, so it asks you to disambiguate | `graph`, `explain`, `why`, `list`, `config` |
| Doesn't scan a module at all | `init`, `migrate`, `new`, `completion`, `version`, `help` |

The second group errors out when `--dir` selects more than one injector, listing the positions it
found:

```
servo: multiple injectors found in this scope — pass --dir to pick one:
  examples/basic/cmd/basic/spec.go:16:2
  examples/basic/cmd/migrator/spec.go:11:2
```

Point `--dir` at one injector's own directory to pick exactly that graph. A `--dir` that selects no
injector — one level too deep, at a package that *calls* an injector rather than holds one — names
where the module's injectors are instead:

```
servo: no servo.Build(...) call in cmd/app/handlers or below — the module's injectors are elsewhere; point --dir at one of them, or at the module root for all of them:
  cmd/app/spec.go:12:2
```

The checks that are about the module rather than one graph read every injector in it, whichever
ones `--dir` selects: a route naming a group only another injector declares is not an undeclared
group, and a malformed spec file anywhere in the module fails every run, exactly as it fails a run
from the root. Which routes an injector *serves* is still its own spec's decision, made with
`servo.HTTP(...)`.

A directory the go command cannot load from is reported in the go command's own words, with the
step that gets past it — no `go.mod` (`go mod init`), a `go.work` directory outside the modules it
lists, a module that does not require servo yet (`go get -tool github.com/okian/servo/v3/cmd/servo`),
a `--dir` that does not exist. See [Loading](diagnostics.md#loading).

### `--scope`

```
--scope module|workspace   which modules are the main module (default module)
```

With a `go.work` listing several modules, the go command treats every one of them as a main
module. `--scope` decides what servo treats as yours:

- **`module`**, the default: the main module is the one `--dir` is in, and no other. A module the
  `go.work` lists beside it is a dependency like any other — interface search never auto-binds its
  types, its `//servo:` routes and `//servo:config` types are not this graph's, and no generated file
  is ever written into it. That is the module `GOWORK=off` builds, which is how CI usually builds.
- **`workspace`**: every module the `go.work` lists is part of the main module — all of each, the
  way the main module itself is loaded — which is what servo did before the flag existed.

Without a `go.work` the two are the same. With one, **generate committed files with the scope CI
uses.** Output generated under `--scope=workspace` can bind a sibling module's implementation or
write a config loader into a sibling's directory, and a `GOWORK=off` checkout, which resolves that
sibling as a plain dependency, then regenerates something else and `servo check` fails. The
command a report suggests repeats `--scope` when it was given, and [`doctor`](#doctor) warns
about a `go.work` with several modules.

`--scope` is accepted by the same eight commands that take the [build flags](#build-flags).

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

They are accepted by the eight commands that load packages — `generate`, `check`, `graph`,
`explain`, `why`, `list`, `config`, `doctor` — and by no others. `init` and `new` write files without loading
anything, and `version` and `help` read nothing at all. (`init` does take `--tags`, but only to
choose the constraint it scaffolds; it never loads a package with them.)
`migrate` walks the tree with `go/parser` and never evaluates a build constraint at all,
so a `--tags` there would be a lie: it reads v1 `Register` calls whether or not a tag would have
excluded the file.

The flags that only change what the compiler or linker *emits* are deliberately absent —
`-race`, `-cover`, `-trimpath`, `-pgo`. Servo runs neither tool. (`-race` does activate the `race`
build tag, so `--tags=race` expresses that case explicitly.)

`GOOS` and `GOARCH` have no flag because the go command has none either; set them in the
environment, as you would for `go build`. With a `servo` binary on your `PATH`:

```
GOOS=linux servo generate
```

With the generator pinned in `go.mod` and run as `go tool servo` — what [`init`](#init) sets up —
the variable has to reach servo without reaching the go command that builds it.
`GOOS=linux go generate ./...`, and `GOOS=linux go tool servo generate`, make go build the tool for
Linux and then fail to run it on this machine: `fork/exec …/servo: exec format error`. Build it for
this machine first, then run the binary with the variable set:

```
GOOS=linux "$(go tool -n servo)" generate
```

`go tool -n servo` builds the host binary if it needs to and prints its path instead of running it;
the command substitution runs before `GOOS=linux` applies to anything.

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

Some build satisfies both constraints at once, and the package would then
declare App and New twice. Servo mirrors each spec file's own constraint and
never invents a negation, so the exclusion has to come from the spec files.
Either gate them so no configuration matches two — `//go:build servoinject && !prod`
on the default spec, `//go:build servoinject && prod` on the other — or, if this
injector does not vary with these tags at all, leave it alone and scope the
run to the one that does with --dir
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
servo generate [--dir <path>] [--scope module|workspace] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file] [-v]
```

Resolves every injector found under `--dir` and writes each one's generated file. **The default
command.** It also scans the module for [`//servo:` route directives](http.md) — module-wide,
alongside the constructor scan — and, for each spec declaring `servo.HTTP(...)`, emits one HTTP
server per served group into that injector's file: mux registration, typed request decoding,
middleware chains, lifecycle.

For each injector it emits its generated file next to that injector's spec file — `servo_gen.go`,
or a [variant name](#variant-file-names) when `--tags` is given — and additionally the
`servo_gen_test.go` override variant when the spec declares at least one `servo.Override` (see
[Generated API](generated-api.md#the-test-variant)). Both files are written atomically — via a
temp file in the same directory and a rename — so a concurrent reader never sees a half-written
file, and a process killed mid-write leaves the previous complete version in place.

**A file whose bytes would not change is not written at all**, so its modification time stays put.
A watcher that runs `servo generate && go build` on every change — air, entr, watchexec — used to
rebuild on servo's own rewrites, around and around with nothing edited.

`-v` lists every generated file on stderr, one line each, as written, unchanged or removed:

```
servo generate: unchanged cmd/basic/servo_gen.go
servo generate: wrote cmd/basic/servo_gen_test.go
servo generate: unchanged cmd/migrator/servo_gen.go
```

Without `-v` it prints nothing on success, except on stderr for a file it removed, a graph with
nothing in it, or generated code it could not compile-check — all described below. On failure it
reports **every** injector that failed, not just the first, each prefixed with its package path:

```
example.com/servodiagnostics/ambiguous: servo: 1 diagnostic(s):

ambiguous/store.go:23:6: servo: ambiguous provider for example.com/servodiagnostics/ambiguous.Store
  needed by *example.com/servodiagnostics/ambiguous.Server  ambiguous/store.go:23:6
  root                                                      ambiguous/spec.go:9:3

  2 types in this module implement Store — pick one with servo.Bind:
      servo.Bind[Store, *Postgres]()  ambiguous/store.go:11:6
      servo.Bind[Store, *Redis]()     ambiguous/store.go:17:6
```

Resolution has exactly two outcomes: a complete plan, or a set of diagnostics. A failed generation
never writes a partial file, and **every graph an injector writes a file for is resolved before
any of its files is written** — the `NewTestApp` graph its `servo.Override` declarations describe
included, which used to be resolved after `servo_gen.go` was already rewritten. A diagnostic from
that graph says which graph it is, since its needed-by chain reads exactly like a production one:

```
example.com/app/cmd/app: in the NewTestApp graph (servo.Override at cmd/app/spec.go:14:3): servo: 1 diagnostic(s):

mock/mock.go:12:6: servo: no provider for *example.com/app/mock.Missing
  needed by *example.com/app/mock.Store  mock/mock.go:12:6
  needed by *example.com/app/comp.Svc    comp/comp.go:15:6
  root                                   cmd/app/spec.go:13:3
```

See [Diagnostics](diagnostics.md) for every message it can produce.

**What it writes, it then compiles.** After the files are on disk, `generate` loads the packages
they landed in once more, under the configuration the generated files are gated for — the user's
tags without `servoinject` — and fails on every error `go build` would stop on inside one of its
files. Most come from the type checker. Some come from the go command before the type checker is
reached — an import of another tree's `internal/` package, an import cycle a generated import
closes — and those are reported the way `go build` reports them, alone, at the import responsible.
The file is left on disk so it can be inspected, and the message names the usual causes, which
are in the code the file was generated from, before it suggests reporting a bug:

```
servo: the generated code does not compile — it was written anyway, so it can be inspected:

  cmd/app/servo_gen.go:22:2: use of internal package example.com/app/svc/internal/db not allowed

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

An error in a file servo did not write — a `main.go` — is not servo's to report and does not fail
the run. Neither does a package whose imports `go list` cannot resolve yet, such as the first
generated import of a config decoder with no `go.sum` line until `go mod tidy` runs: every error
after a failed import describes the import, not the generated code. But that package was not
compiled, and `generate` says so rather than passing it in silence:

```
servo generate: warning: could not compile the generated code in example.com/app/cmd/app: no required module provides package gopkg.in/yaml.v3; to add it: go get gopkg.in/yaml.v3
servo generate: run `go mod tidy`, then `servo generate` again to check that it compiles
```

**Generated files nothing produces any more are removed** — only files carrying servo's own
generated-code header, and always with a line on stderr:

- a `servo_gen_test.go` beside a spec that no longer declares `servo.Override`. Nothing would
  ever regenerate it once the last `Override` is gone, yet `go test` keeps compiling it.
- a `servo_config_gen.go` whose package no longer declares a `//servo:config` type, because the
  type moved to another package or its directive was deleted. It still compiles into every build,
  and fails it with `undefined: Config`. `generate` looks for these across the whole module, not
  only in the packages a graph uses, because the commonest one sits in a directory that holds
  nothing else, which the load never sees. A directory this build configuration excludes but that
  holds other Go files — a package gated behind a build tag — is left alone: nothing in this run
  can say whether its type still exists.

```
servo generate: removed cmd/app/servo_gen_test.go — the spec no longer declares servo.Override
servo generate: removed internal/oldcfg/servo_config_gen.go — its package no longer declares a //servo:config type
```

**An empty graph gets a note.** The empty `servo.Build()` that [`servo init`](#init) scaffolds
generates and builds cleanly into a program whose `Run` returns the moment it starts. A spec with
no `servo.Root` and no `servo.HTTP()` draws one line on stderr, and the run still succeeds:

```
servo generate: note: example.com/app/cmd/app declares no servo.Root and no servo.HTTP(), so its App constructs nothing and Run returns at once — add a servo.Root[T]() for each component the program runs
```

## `check`

```
servo check [--dir <path>] [--scope module|workspace] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]
```

Verifies that every injector's committed generated files match what a fresh generation would
produce, and that the ones that match compile. Never writes anything. With `--tags` it checks that
configuration's [variant](#build-variants), so a module with several needs one run per variant.

This is the CI command. A constructor signature change without a matching re-run of
`servo generate` fails here instead of shipping a stale generated file. It reports every stale
injector in one run, with a unified diff for each:

```
servo check: cmd/migrator/servo_gen.go is stale — run `servo generate --dir=examples/basic`
--- cmd/migrator/servo_gen.go (committed)
+++ cmd/migrator/servo_gen.go (fresh)
@@ -170,6 +170,6 @@ func (a *App) Graph() servo.Graph {
 	}}
 }
 
-func (a *App) Report() servo.StartupReport { // hand-edited
+func (a *App) Report() servo.StartupReport {
 	return a.startupReport
 }

note: this is servo v3.5.0. If regenerating does not settle it, the machine that
      committed the file was running a different version — compare `servo version`, and
      pin one for everybody with `go get -tool github.com/okian/servo/v3/cmd/servo`.
```

Each hunk has three lines of context and, after its line numbers, the declaration line its first
change sits under. In a generated file that is what places a one-line change: every rollback path
holds the same few stop calls under a different function name, and a bare `+	_ =
a.stopIssuer(ctx)` could be any of them. The diff stops after 400 lines with a count of what it
left out — a file that differs that much has changed shape, and regenerating and running `git
diff` shows all of it.

The command a report names repeats the flags `check` was given — `--dir`, `--scope` and the build
flags — with the variant's own `--tags`, so it works from wherever `check` ran. A bare
`servo generate`, run at a repository root where CI checked `--dir=services/api`, fails with
"run `servo init`".

The trailing note is on every stale report, because "stale" has exactly one other cause and the
diff cannot tell them apart. A change to generated code's internal shape is deliberately *not* a
breaking change — consumers regenerate — so two machines on two servo versions produce a real
difference in a file neither of them edited, and it reads exactly like a forgotten regenerate.
Without the note the loop is: CI says stale, the developer runs the command it names with their own
binary, pushes, CI says stale again. The version it prints is [`servo version`](#version)'s, so the
two can be compared directly.

A missing generated file is reported distinctly from a stale one:

```
servo check: cmd/basic/servo_gen.go does not exist — run `servo generate`
```

**Line endings are not a difference.** Git for Windows checks text files out with CRLF line
endings by default (`core.autocrlf=true`), which used to make every generated file stale on every
Windows checkout. `check` compares the committed file with its line endings normalized, so a file
that differs only in those is current. To keep the working copies LF on every platform too — and
to collapse servo's files in GitHub's diffs — mark them in `.gitattributes`:

```
servo*_gen*.go  text eol=lf linguist-generated
```

**What matches, it then compiles**, the way `generate` compiles what it writes. A committed file
byte-identical to a fresh generation can still not compile — a `servo.Bind` to a mock whose
interface has since grown a method generates the same broken file on every run — and comparing
bytes alone called that current while `go build` failed. A compile error is reported with
`generate`'s message, and a package that could not be compiled draws `generate`'s warning. The
cost is one more load of the injector packages, about a second on the tutorial. A broken file
exits 1, as a stale one does, so the [exit-code contract](#invocation) is unchanged: a CI step that
runs `servo check` needs nothing new.

The override variant is compared the same way: with a `servo.Override` declared, a stale or
missing `servo_gen_test.go` is reported exactly as the production file is, and with none declared
a `servo_gen_test.go` servo once wrote is refused as left over — `generate` removes it. So is a
`servo_config_gen.go` whose package no longer declares a `//servo:config` type:

```
servo check: internal/oldcfg/servo_config_gen.go is left over from a //servo:config type its package no longer declares — run `servo generate`, which removes it
```

One thing `check` does not do is verify that the generated files are committed to VCS; that is
`doctor`'s job.

## `graph`

```
servo graph [--dir <path>] [--scope module|workspace] [--format=text|json|dot|mermaid]
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

**`json`** — the stable machine format, and byte-for-byte the same schema the generated
`App.Graph()` serialises to. Both paths populate the identical `servo.Graph` struct, documented
field by field in [servo package](servo-package.md#graph-and-graphnode):

```json
{
  "nodes": [
    {
      "type": "*example.com/servobasic/logger.Logger",
      "level": 1,
      "deps": null,
      "capabilities": ["Finalizer"],
      "binding": "sole candidate",
      "pos": "logger/logger.go:10:6"
    }
  ]
}
```

Two details there are load-bearing, and both exist so a consumer written against one producer works
against the other. An empty `deps` is `null`, not `[]` — the generated `App.Graph()` emits a Go
literal, where the empty slice is `nil`, and a CLI that wrote `[]` beside `null` for an equally
empty `capabilities` was not even self-consistent. And `pos` is relative to the module root (or,
for a provider outside it, its package's import path plus the file name), the same rewriting
emission does, rather than the absolute path `explain` and `list` print: an absolute path would
embed the machine's filesystem layout in output the generated file has to match.

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

### HTTP in the graph output

When the injector declares [`servo.HTTP()`](http.md), the `json` format gains a top-level `http`
object — [`servo.GraphHTTP`](servo-package.md#graphhttp): the served groups, every route with its
method, pattern, group, handler and arguments (extracted parameters marked `(extracted)`), each
`servo.Use` attachment with what it wraps (`"server"`, `"group x"`, `"route METHOD /p"`), and the
extractors. Like `scopes`, it is `omitempty`, so a graph without HTTP emits exactly the JSON it
always did. The `text`, `dot` and `mermaid` formats are unchanged: routes and servers are emitted
machinery, not nodes, so there is nothing for an edge to point at.

An unrecognised format is an error: `servo graph: unknown --format "svg" (want text|json|dot|mermaid)`.

## `explain`

```
servo explain [--dir <path>] [--scope module|workspace] [--json] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file] <type>
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
| `explicit bind` | A `servo.Bind` (or `Override`) named this concrete type. The line names the interface and where the Bind is written — `, via servo.Include` when it came from an included marker set rather than the spec file |
| `sole candidate` | Exactly one function in the module returns this exact type |
| `sole implementation` | The parameter is an interface, and exactly one candidate in the main module implements it. The line names the interface |

Asking about the interface itself answers the same question from the other end. Nodes are keyed by
the type a provider returns, but the type you read in a constructor's parameter list is often an
interface, so `explain` resolves it to the node the graph hands every consumer of it, and says how
first:

```
$ servo explain --dir examples/basic/cmd/basic store.Store
example.com/servobasic/store.Store resolves to *example.com/servobasic/postgres.DB — explicit bind, servo.Bind at cmd/basic/spec.go:20:3

*example.com/servobasic/postgres.DB
  provider:     postgres.New (postgres/postgres.go:13:6)
  binding:      explicit bind (example.com/servobasic/store.Store, cmd/basic/spec.go:20:3)
  lifetime:     singleton — one per process, built by New
  level:        2
  depends on:   *example.com/servobasic/logger.Logger
  depended on:  *example.com/servobasic/api.Server
  capabilities: Initializer, Finalizer, Healther
```

The other two answers are `the sole implementation in the main module`, and, for the interface a
`servo.Scoped` declares, `the scope accessor servo generates for the servo.Scoped at …` — the scoped
node it hands out is what is described.

**How `<type>` is matched.** An exact match against the node's full type string wins. Failing
that, servo looks for a node whose type string *ends with* the argument — so `api.Server` finds
`*example.com/servobasic/api.Server` without you typing the import path. Only when neither finds
anything does it try the spellings a key does not contain:

- **A pointer, as Go writes it.** `*api.Server` matches pointer nodes whose element matches
  `api.Server`, and never a value of the same name.
- **A type as source code spells it,** with package names where the key has import paths — which
  is how a generic instantiation is written: `repo.Repo[model.User]` finds
  `*example.com/app/repo.Repo[example.com/app/model.User]`. Spacing inside the brackets doesn't
  matter, and a bare `repo.Repo` finds the instantiation in the graph, or names every one.
- **An interface the graph asks for,** as above — through a constructor parameter, a `servo.Root`, a
  handler argument, a `servo.Use`, or a scope extractor.

An ambiguous match is an error rather than a guess:
`servo: "Server" matches multiple nodes, be more specific: ...`.

**When the type is not in the graph, `explain` says why** instead of only that it isn't:
the constructor that provides it but that nothing in this graph depends on — so no root reaches it,
and the `servo.Root[...]` that would add it — the `servo.Override` that puts it only in
`NewTestApp`'s graph, the candidates implementing an interface nothing asks for, or a function that
returns it but was rejected, with the rule that rejected it:

```
$ servo explain --dir examples/basic/cmd/basic mockstore.Store
servo: no node matches "mockstore.Store" — example.com/servobasic/mockstore.Store is not in this graph
  mockstore.New (mockstore/mockstore.go:29:6) provides *example.com/servobasic/mockstore.Store, but nothing in this graph depends on it, so no root reaches it — declare servo.Root[*mockstore.Store](), or take it as a constructor parameter
  servo.Override at cmd/basic/spec.go:21:3 puts it in NewTestApp's graph in place of example.com/servobasic/store.Store; explain and why answer for New's
```

Only types in packages the run loaded are known: the whole main module, whatever `--dir` selects,
and everything it imports. The exit status is 1 either way.

`--json` prints the same information as an object with `type`, `provider`, `pos`, `binding`,
`lifetime`, `level`, `depends_on`, `depended_on` and `capabilities`, plus `scope` when the node is
scoped, `binds` — one `{interface, pos, included}` per `servo.Bind` that selected the node — when
`binding` is `explicit bind`, and `via` — `{interface, binding, pos, included}` — when the query
named an interface. The three are omitted when they don't apply.

## `why`

```
servo why [--dir <path>] [--scope module|workspace] [--json] [--all] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file] <type>
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
and the shorter one is reported. `--all` lists every path instead, shortest first:

```
$ servo why --all logger.Logger
root  *example.com/servobasic/worker.Consumer
  -> *example.com/servobasic/logger.Logger

root  *example.com/servobasic/api.Server
  -> *example.com/servobasic/postgres.DB  (as example.com/servobasic/store.Store, explicit bind)
  -> *example.com/servobasic/logger.Logger
```

Every root's paths come first, then every path from an HTTP declaration (below) — every reason the
node is in the binary. Paths multiply through shared nodes, so `--all` stops at 100 and says so;
past that, `explain` on a node along them is the better question.

A step taken through an interface says so, as the `postgres.DB` step above does: `api.Server` takes
`store.Store`, not `*postgres.DB`, and a path that left the interface out would answer "why" with a
parameter list nobody wrote. The note gives the interface and how it resolved, as
[`explain`](#explain) words it. A path into a scope goes through the accessor, and names it:

```
$ servo why --dir examples/scoped example.com/servoscoped/chat.Room
root  *example.com/servoscoped/api.Server
  -> *example.com/servoscoped/chat.Room  (acquired via example.com/servoscoped/chat.Rooms)
```

`api.Server` takes `chat.Rooms`, not `*chat.Room`. The accessor is generated code rather than a
resolved node, so — as in [`graph`](#graph)'s `dot` and `mermaid` output — the edge is collapsed
onto the scoped type it hands out, and the note names the interface the consumer actually holds.

A node that resolved but isn't reachable from any root is reported as such:
`servo why: <type> is not reachable from any root`. Type matching works exactly as in
[`explain`](#explain), interfaces included, and so does the explanation when the type is not in the
graph at all. `--json` prints the path as an array of type strings; with `--all`, an array of such
arrays.

[Routes, middleware and extractors](http.md) are declaration-driven, not reached through another
node's constructor — a route is not a graph node at all. A dependency only the HTTP surface pulls
in therefore descends from no `servo.Root`, and `why` answers with the declaration that asked for
it instead: the handler, the `servo.Use` attachment, the `servo.Extract`, or `servo.HTTP()` itself
for the server config.

```
$ servo why --dir examples/http store.Store
root  handler api.Order, POST /order/{category}/
  -> *example.com/servohttp/store.Store
```

With `--json` that entry point is the array's first element, ahead of the type strings. `explain`
lists the same declarations among a node's dependents, in parentheses, the way it lists the
accessor a scoped node is acquired through.

## `list`

```
servo list [--dir <path>] [--scope module|workspace] [--rejected] [--all] [--json]
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

In a module with [`//servo:` route directives](http.md), the text output ends with a `routes:`
section — routes are the other thing servo sees, and "why doesn't servo serve my endpoint" is the
same question as "why isn't my constructor indexed". One line per directive: method and pattern,
the group in brackets when there is one, the handler, its position. Module-wide, like the scan
itself:

```
routes:
  POST /order/{category}/                  api.Order                 api/api.go:94:1
  GET /healthz [telemetry]                 api.Healthz               api/api.go:158:1
```

An injector declaring [`servo.Trace`](tracing.md) ends with a `traced:` section as well — per
spec rather than module-wide, since which consumers get a wrapper is this injector's own
declaration: the interface, its `Expected` sentinels when there are any, and the marker's
position:

```
traced:
  example.com/app/store.Store              expected: store.ErrMissing  cmd/app/spec.go:21:3
```

The `--json` schema is unchanged — still the bare candidate array — so nothing consuming it needs
updating.

`--rejected` is the higher-value mode, and the first thing to reach for when you wrote a
constructor and servo doesn't see it — though a "no provider" diagnostic now names the
[near misses](resolution.md#near-misses) for the type it is about. It lists every function that
*looked* like it might be a provider, the type it would have provided, and the rule that excluded
it; then the candidates this injector [cannot refer to](resolution.md#the-injector-has-to-be-able-to-refer-to-it);
then the methods, grouped by receiver — every `Init`, `Stop` and `Health` lands there, and listed one
per line they were most of the output:

```
$ servo list --rejected
auth.HashPassword  internal/auth/auth.go:82:6    string          result type is a primitive (string)
a.NewClient        internal/a/a.go:7:6           *a.Client       variadic parameter — unsupported

candidates example.com/app/cmd/app cannot use:
comp.NewPool       internal/comp/comp.go:6:6     *comp.pool      its result type *comp.pool is unexported, so the App generated in example.com/app/cmd/app cannot name it — …

methods are never providers (66, by receiver):
  api.(*Server)      Handler, Run, Ready, Stop
  postgres.(*Store)  Init, Stop, Health, Create, Get
```

Every reason string is enumerated in
[Resolution rules](resolution.md#rejection-reasons). Functions with **no** results at all are
neither accepted nor rejected — they aren't trying to construct anything, and listing them would
bury the real answers under every helper in the module. A candidate the injector cannot use is
still in the plain `servo list`, which is the injector-independent index.

`--all` includes stdlib and third-party packages. Both modes default to the main module only,
because someone asking why their constructor wasn't picked up is never asking about
`unicode.ToLower`. `--json` prints `{name, pos}` objects, or, with `--rejected`,
`{name, pos, reason, provides, kind}` — one object per entry, methods included, `kind` being
`function`, `method` or `unusable` (`provides` and `kind` are a schema **addition**).

## `config`

```
servo config [--dir <path>] [--scope module|workspace] [--json]
```

Prints the operator's manual for one injector: every setting its
[`//servo:config`](config.md) types read, resolved from the actual graph — so a config type
nothing depends on doesn't clutter the table, exactly as it doesn't reach the binary:

```
$ servo config
configuration for example.com/orders/cmd/orders
file: config.yaml (override the path with CONFIG_FILE; environment always wins)

  ENV                  FILE KEY            TYPE             MISSING?      FIELD
  POSTGRES_DSN         postgres.dsn        string           required      dbConfig.dsn  postgres/postgres.go:14
  POSTGRES_MAX_CONNS   postgres.max_conns  int32            default 10    dbConfig.maxConns  postgres/postgres.go:15
  POSTGRES_PASSWORD    postgres.password   string (secret)  required      dbConfig.password  postgres/postgres.go:17
```

The `FILE KEY` column appears only when the spec declares a
[`servo.ConfigFile`](spec.md#configfile); `MISSING?` is what happens when no source supplies the
setting — `required`, `default <value>`, or `zero value`. Only the generator can print this table
without running the binary: it is the same information the generated loader was built from.

`--json` emits one object per setting: `{env, file_key, type, required, default, secret, field,
pos}`. Rows are sorted by environment variable either way. Like `explain` and `why`, this is a
question about one graph — pass `--dir` when the scope holds more than one injector.

## `init`

```
servo init [--dir <path>] [--tags tag,list]
```

Scaffolds **two** files in `--dir` — three, when the directory is a `package main` with no `main`
function yet — and prints the one-time module setup they need:

```
$ servo init --dir cmd/app
servo init: wrote cmd/app/servo_spec.go
servo init: wrote cmd/app/servo_generate.go
servo init: wrote cmd/app/main.go

Next, once per module, so that go generate can run the generator and so
that every machine runs the same one:

    go get -tool github.com/okian/servo/v3/cmd/servo

Then declare your roots in cmd/app/servo_spec.go and run:

    go generate ./...
    go run ./cmd/app
```

**`servo_spec.go`** — the spec file, with the build tag and the package clause already correct, and
no `go:generate` directive in it:

```go
//go:build servoinject

package main

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		// servo.Root[*yourpkg.YourType](),
	)
}
```

**`servo_generate.go`** — untagged, and holding nothing but the directive:

```go
package main

//go:generate go tool servo generate
```

**The directive cannot live in the spec file.** `go generate` honours build constraints, so a
directive inside a `//go:build servoinject` file is invisible to `go generate ./...` — which then
exits 0, prints nothing, and generates nothing. Silence is the worst failure mode available to a
tool whose whole claim is build-time checking, so the directive gets its own untagged file. There is
one such file per directory rather than one per variant, because `go generate` has no build tags to
select between them and a second copy would just run the same generation twice.

**And it is `go tool servo`, not `go run <module path>`,** for the reason under
[Installing](#installing): the generator's dependencies are not in a consumer module's build list.
That is what the `go get -tool` line is for, and why `init` prints it.

**`main.go`** — the documented one, so that `go generate ./... && go run ./cmd/app` works in the
first minute rather than after the reader has gone looking for how a generated `App` is run:

```go
package main

import "github.com/okian/servo/v3/servo"

// servo.Main builds the App with New, runs it until the process is told
// to stop, shuts it down within a deadline, and exits non-zero when
// anything along the way failed.
func main() { servo.Main(New) }
```

It is written only into a `package main`, only when no file in the directory already declares a
`main` function — whatever that file is called, since a second one would not compile — and never
over an existing `main.go`. [`servo.Main`](servo-package.md) is the whole program around a
generated `App`: signals, the run, the shutdown deadline, the exit status.

The package name is taken from any existing `.go` file in the directory, falling back to `main` —
the usual case of a spec file landing next to a `cmd/*/main.go`. The directory is created if it
doesn't exist.

It refuses to overwrite the spec: `servo init: <path> already exists`. `servo_generate.go` is
written only when absent, and left untouched otherwise.

With `--tags`, it scaffolds a [variant](#build-variants) instead: `servo init --tags=prod` writes
`servo_spec_prod.go` gated `//go:build servoinject && prod`. When it also has to write the directive
file, that carries the matching flags — `go tool servo generate --tags=prod`. When one is already
there from an earlier `init`, it is left alone, so a second variant means adding its
`//go:generate` line to that file yourself: each configuration needs its own generation, and the
file is untagged, so both lines live in it side by side. `init` then names any sibling spec still
visible under those tags, since leaving the default spec ungated is what makes two variants collide:

```
servo init: wrote cmd/app/servo_spec_prod.go
servo init: servo_spec.go is also visible with --tags=prod, so both would generate a file and the two would compile together.
            Narrow it to `//go:build servoinject && !prod` (or otherwise exclude the new tags) before running servo generate.
```

## `doctor`

```
servo doctor [--dir <path>] [--scope module|workspace] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]
```

Diagnoses setup problems before `go generate` is ever run, across every injector in scope. Every
line is `[OK  ]`, `[FAIL]`, `[WARN]`, or `[INFO]`. Only `[FAIL]` affects the exit status — one is
enough to make the command exit 1 with `servo doctor: problems found`. `[WARN]` is a problem doctor
can name but not judge — a version mismatch, a workspace, a module that regenerates without go
generate — and `[INFO]` reports a fact worth knowing rather than judging: routes no injector serves,
the variant inventory, a version that cannot be compared. Neither ever fails the run.

```
$ servo doctor --dir examples/variants
servo doctor:
  [INFO] go.mod replaces github.com/okian/servo/v3 with ../.., so this servo ((devel, 71f44864ed6c)) has no release to be compared against
  [OK  ] no build errors outside the injector(s)
  [OK  ] examples/variants/go.mod declares the servo tool that `go tool servo` runs
  [OK  ] spec file found at cmd/app/spec.go:18:2, correctly gated by the servoinject build tag
  [OK  ] go generate runs `servo generate` for this injector: cmd/app/servo_generate.go:7
  [OK  ] generated file present: cmd/app/servo_gen.go
  [OK  ] generated file matches a fresh generation
  [OK  ] generated file is tracked by git
  [INFO] not checked by this run, being other variants: servo.prod_gen.go (`servo generate --tags=prod`)
  [OK  ] the generated files that match a fresh generation compile
```

What each check means:

| Check | Fails when |
| --- | --- |
| Module loads | `go/packages` can't load the module at all |
| Spec file found | No `servo.Build(...)` call, or one in a file without the `servoinject` constraint |
| Servo version | Never fails. An `[OK  ]` when this binary is the release `go.mod` requires; a `[WARN]` when it is another one; an `[INFO]` when either side has no release to compare — a development build, or a `go.mod` that replaces servo |
| Workspace | Never fails — a `[WARN]` when a `go.work` lists more than one module (below) |
| No build errors outside the injector(s) | Some *other* package doesn't type-check. Errors inside an injector's own package are deliberately ignored: before the first generation, `main.go` legitimately references a `New` that doesn't exist yet |
| Tool directive | A `//go:generate` line runs `go tool servo`, and `go.mod` has no `tool github.com/okian/servo/v3/cmd/servo` directive, so `go generate` would stop at `no such tool "servo"`. Checked only when some line runs `go tool servo` |
| Injector visible in this configuration | A package holds a spec file these flags exclude, so nothing generates its `New`. Reported here and nowhere else — see [Checking variants](#checking-variants) |
| Config loaders agree | Two injectors use one `//servo:config` type and only one declares `servo.ConfigFile` — the refusal [`generate`](#generate) and [`check`](#check) make, since the companion loader is one file with one signature |
| No leftover config loader | A `servo_config_gen.go` written for a `//servo:config` type its package no longer declares — the search [`check`](#check) makes, one `FAIL` per file, naming the `generate` that removes it |
| go generate reaches it | Per injector, for the variant these flags select (below) |
| Graph resolves | The injector's graph does not resolve. Reported as that, with the diagnostics, rather than as a stale file — regenerating cannot fix it |
| Generated file present | The generated file — `servo_gen.go`, or the [variant](#build-variants) matching `--tags` — is missing next to the spec |
| Generated file fresh | Same comparison [`check`](#check) makes |
| Fresh files compile | A generated file identical to a fresh generation fails to compile — checked once, for every injector's current files together, the way [`check`](#check) checks them. Byte-identical is not buildable: a provider only a `servoinject`-gated file declares generates the same broken file every time |
| Override variant | With a `servo.Override` declared, `servo_gen_test.go` is missing or stale; with none declared, one servo once wrote is still there — `generate` removes it |
| Tracked by git | A `[WARN]`, never a `FAIL` — best-effort, so no git, no repo, or a different VCS just means "can't tell" |
| Route groups declared | A [`//servo:` directive](http.md)'s trailing token names a group no spec declares: ``group "x" is not declared by any injector`` |
| Routes have a server | An `[INFO]`, never a `FAIL` — routes exist but no injector in this configuration declares `servo.HTTP()`, so nothing serves them. Legitimate when a variant spec declares it under flags this run wasn't given, which is exactly why it informs rather than fails |
| No orphaned generated file | A `servo.<tags>_gen.go` beside the spec that no spec file in that directory could produce any more |
| Variant inventory | Never fails — an `[INFO]` line naming the sibling variants these flags did not check, and the command that would |

The generated file *should* be committed, which is what the git check is nudging: a checkout should
build without anyone having to run `servo generate` first. Every command doctor suggests repeats
the `--dir` and build flags it was run with, so the suggestion works from where you typed it.

**Whether `go generate` reaches the generator** is read the way `go generate` reads it: every
`//go:generate` line in the module, in the files `go generate` would see — build constraints
evaluated with these flags' tags plus `generate`, test files included — with `-command` shorthands,
quoting and `$VARS` honoured. A line counts for an injector when it runs servo's `generate` —
`go tool servo`, `go run` of its package, or a `servo` binary — over a `--dir` containing the
injector, with the `--tags` of the variant being checked; its arguments are parsed with
`generate`'s own flags. Four outcomes:

- **`[OK  ]`** — such a line exists, and its position is printed.
- **`[FAIL]`, the line never runs.** The only covering line is in a file `go generate` skips: the
  spec file, most often, whose `//go:build servoinject` is false when `go generate` evaluates it.
  `go generate ./...` then exits 0 having generated nothing, which is why `init` writes the
  directive into a file of its own:

  ```
  [FAIL] go generate never runs `servo generate` for this injector: servo_spec.go:5 is in a file go generate skips, because its build constraint (`//go:build servoinject`) is false there — go generate honours build constraints. Move the line to an untagged file; servo init writes one, servo_generate.go
  ```

- **`[FAIL]`, no line covers it.** The module's other lines run servo, so it uses `go generate` for
  this — but none covers this injector, or covers it only for another variant. A covering line servo
  would refuse, such as a misspelled flag, is a `[FAIL]` of its own.
- **`[WARN]`, the module never runs servo from `go generate`.** Regenerating from a Makefile or a CI
  script is a legitimate setup, and a doctor that could never go green for it would be switched off,
  so this says what `go generate ./...` will not do and how `init` would change that.

**The version line comes first** because every freshness verdict below it is a comparison with what
*this* binary would generate, and that is the module's answer only when this binary is the servo
`go.mod` builds against. A global `servo` installed `@latest` beside a module pinned with
`go get -tool` is the normal way to get a mismatch:

```
  [WARN] this servo is v3.4.1, but go.mod requires github.com/okian/servo/v3 v3.5.0 — a generated file is only fresh relative to the generator that wrote it, so run the module's own: `go tool servo` (after `go get -tool github.com/okian/servo/v3/cmd/servo`). Files generated since v3.5.0 also carry a compile-time contract guard (servoimpl.Contract1), so code from a generator newer than the runtime stops at an undefined servoimpl constant instead of misbehaving at run time
```

A stale-file `[FAIL]` under that `[WARN]` says the version may be why.

**A `go.work` listing several modules** is a `[WARN]`, because whether a sibling module is part of
the graph is then a choice. Each injector resolves against its own module by default
([`--scope=module`](#--dir-and-injector-scope)), so another listed module's types are never
auto-bound and no generated file is written into it; `--scope=workspace` makes every listed module
part of it. CI usually builds without the `go.work` (`GOWORK=off`), so generate the way CI will.

The last two exist because everything above them concerns only the one variant the current flags
select, which leaves a project's other variants invisible to every servo command. An orphan — a
generated file whose spec was deleted while it stayed behind — is the sharper of the two: it keeps
compiling into whichever build satisfies its constraint, nothing will ever regenerate it, and so it
drifts silently from the moment its spec went away. That is a `FAIL`:

```
$ servo doctor
  [FAIL] servo.prod_gen.go is generated from a spec that no longer exists — delete it, or
         restore the spec file it came from
```

The inventory is the softer half — a stale `prod` variant nobody ran `--tags=prod` against draws
three green servo commands next to a red `go build`, so `doctor` names it and the command that
would check it:

```
$ servo doctor --dir examples/variants
  [INFO] not checked by this run, being other variants: servo.prod_gen.go (`servo generate --tags=prod`)
```

`[INFO]` marks a fact worth knowing that fails nothing. The unserved-routes line above is one:

```
  [INFO] 3 //servo: route(s) found, but no injector in this configuration declares servo.HTTP(), so nothing serves them
```

The other is the variant inventory — a run only checks the variant its flags select, so doctor
names the generated files it saw and did not verify (`not checked by this run, being other
variants: …`).

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
servo new component [--package name] <Name>
servo new adapter <pkgname>
servo new mock-adapter [--package name] <moq|mockery|gomock> <GeneratedTypeName>
```

Prints a complete Go file to stdout — package clause, imports and all — that compiles as printed,
and resolves under `servo generate` once something roots it. It never writes a file, never takes
`--dir`, and never imports `servo`. Redirect it where you want it:

```
servo new adapter redisx > internal/redisx/redisx.go
```

**`component`** prints a type, its constructor, and all seven capability methods commented out,
ready to uncomment whichever the component actually needs — importing `context` along with the
first, which is why the file imports nothing as printed. Its package is `--package`, or the type
name lowercased: `servo new component Widget` prints `package widget`.

**`adapter`** prints the wrapper a third-party client needs before the graph can depend on it,
in the package its argument names: the client's settings as an unexported `config` struct marked
`//servo:config prefix=<PKGNAME>` — an `addr` that is required and a `timeout` defaulting to `5s` —
so generation writes their loader instead of failing on a config nothing provides; a `Client`;
`func New(cfg config) (*Client, error)`; and `Init` to connect, `Stop` to release, `Health` to
report. Teardown is `Stop` alone. (It used to return a cleanup func as well — a second way to
release the same connection — and its exported `Config` had no provider, so rooting it failed with
`no provider for redisx.Config`.)

**`mock-adapter`** prints the small hand-written file a generated mock needs before it can be a
provider. Each of the three tools needs a different fix, for a different reason — `moq` generates
no constructor at all, while mockery's and gomock's constructors take a per-test value the graph
has no way to supply — and this subcommand knows which is which. `mockgen` is accepted for
`gomock`. The file goes beside the generated mock, in its package: `--package`, or `mocks` —
mockery's default output package — when none is given. The full explanation, with runnable
examples, is in the README's
[Mocking section](https://github.com/okian/servo/blob/master/README.md#mocking).

An unknown kind or tool is an error naming the valid set, and so is a name the file could not
use: a type name has to be an exported Go identifier — the spec naming it lives in another
package — and a package name a valid one.

## `completion`

```
servo completion <bash|zsh|fish>
```

Prints a completion script for the shell, to stdout: command names, each command's flags, the
values `--format` and `--mod` take, directories for `--dir` and files for `--modfile` and
`--overlay`, `new`'s kinds and mock tools, and `help`'s topics. Load it for the current shell with
one of:

```
source <(servo completion bash)
source <(servo completion zsh)
servo completion fish | source
```

or save the zsh script as `_servo` in a directory on `$fpath`. Everything in the script is read off
the same table of commands and flag sets the parser uses, so a flag a newer servo adds completes
after regenerating the script, with nothing to edit by hand. It completes the `servo` command;
`go tool servo` is the go command's, and these scripts leave it alone.

## `version`

```
servo version
```

Prints one line: the servo version, the Go toolchain that built the binary, and its target
platform.

```
$ servo version
servo v3.2.1 go1.27.0 darwin/arm64
```

This matters more than it does for most tools. Servo writes files you commit and gates them with
[`check`](#check), so two machines on two servo versions produce a diff in a file neither of them
edited — which is indistinguishable, from the diff alone, from a forgotten regenerate. `check`'s
stale report names this command for exactly that reason.

A binary built straight from a working tree has no module version — `debug.ReadBuildInfo` reports
`(devel)` — so the VCS stamp the go command embeds is printed instead, which is the only thing that
distinguishes two such builds from each other: `(devel, 71f44864ed6c)`, or
`(devel, 71f44864ed6c, dirty)` when the tree had uncommitted changes.

A module that replaces servo — a fork, a local checkout, this repository's own examples — records
the requirement as the version and what was built as the replacement, and the replacement is what
is printed: `(devel, replaced by ../..)` for a directory, or the replacing module's version and
path. `go tool servo version` in `examples/variants`, which requires v3.0.0 and builds `../..`,
used to print v3.0.0.

`servo --version` prints the same line.

## `help`

```
servo help [command]
servo -h | --help | -help
servo <command> -h | --help
```

Prints the command list, the shared [build flags](#build-flags), and a link to this reference, to
stdout, exiting 0. With a command name it prints that command's usage line, its summary and every
flag with its default instead, which is also what `servo <command> -h` prints:

```
$ servo help check
usage: servo check [--dir path] [--scope module|workspace] [--tags tag,list] [--mod mode] [--modfile file] [--overlay file]

verify every generated file matches a fresh generation; writes nothing

Flags:
  --dir path                path to scan from; every injector found within it is checked (default ".")
  --scope module|workspace  which modules are the main module, one of module|workspace: the one --dir is in, or every module go.work lists (default "module")
  --tags tag,list           a comma-separated tag,list of additional build tags to consider satisfied during the load
  --mod mode                module download mode to use: readonly, vendor or mod; defaults to the go command's own choice
  --modfile file            read an alternate go.mod file instead of the one in the module root directory
  --overlay file            read a JSON config file that provides an overlay for build operations
```

The usage line, the flag list, the parser and the dispatch are all read off one table of commands,
so a flag a command accepts is on its usage line by construction — the separately written synopses
they replaced had drifted to advertising `servo explain <type> [--dir]`, an order the parser then
refused.

An unknown command prints the same list rather than only saying the name was wrong — being told
`unknown command "geneate"` and nothing else is a dead end when the nearest list of the thirteen names
is a website away:

```
$ servo geneate
servo: unknown command "geneate"

Commands:
    generate  resolve every injector found under --dir and write its generated file
    …

Run `servo help` for flags and usage.
```

That path exits 1. An unknown *topic* (`servo help geneate`) is the same error, again with the list.

## `servo-vet`

> **Build tags and `servo-vet`.** Run standalone, it analyses only the default configuration, and
> `-tags` is **refused**, with exit code 2. `go/analysis` registers that flag on every
> `singlechecker` binary and documents it as "no effect (deprecated)": `checker.Run` builds its own
> `packages.Config` with no build flags, so `servo-vet -tags=prod ./...` would exit 0 having
> analysed the default configuration while anyone who typed it believes `prod` was covered. There is
> no hook to make it work — the config is internal to `x/tools` — so the honest move is to refuse
> and name the invocation that does:
>
> ```
> $ servo-vet -tags=prod ./...
> servo-vet: -tags does not work here — it is go/analysis's own no-op flag, so this run would
> silently analyse only the default configuration.
>
> To check a tagged configuration, drive servo-vet through the go command, which does understand
> build flags:
>
> 	go vet -tags=prod -vettool=$(which servo-vet) ./...
> ```
>
> The refusal is by inspection of `os.Args`, so it catches `-tags`, `--tags`, `-tags=prod` and
> `-tags prod`, wherever they sit in the argument list. `-tags` with no value is left to
> `go/analysis`'s own parser, and an invocation driven by the go command — which forwards the flag
> to the tool along with the `.cfg` file describing packages it has already loaded under those
> tags — is recognised and let through, since that is the invocation that works.

```
go get -tool github.com/okian/servo/v3/cmd/servo-vet
go tool servo-vet ./...
```

A standalone [`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis) analyzer (named
`servovet`) for the servo mistakes the compiler cannot catch. `go install
github.com/okian/servo/v3/cmd/servo-vet@latest` works too; `go run` against the module path does
not, for the [same go.sum reason](#installing) it doesn't for `cmd/servo`.

**A marker call without the build tag.** Calls to `servo.Build`, or to any `servo` function
returning `Marker`, `HTTPOption`, `ScopeOption` or `TraceOption` — `Root`, `Bind`, `Override`,
`Scoped`, `Linger`, `Max`, `Value`, `Include`, `ConfigFile`, the [HTTP markers](http.md) and the
[tracing ones](tracing.md) alike — in any file that doesn't carry a build constraint requiring
`servoinject`. The analyzer recognises a marker by its result type, so a marker added later is
covered without a change to it, and by what the call resolves to, so a dot-imported, bare `Build(…)`
is covered too. The markers panic when actually executed, so such a call compiles straight into your
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
name is left alone.

**A malformed or misplaced `//servo:` comment.** The whole [directive prefix](http.md#the-directive)
is reserved, and the analyzer runs `servo generate`'s own pass over every line under it, in the
editor, where the typo is cheapest to fix: the same tokenizer (a tab or a space after the colon
reads the same to both), the same placement rules (`//servo:config` on a `var`, a route verb off a
function), the same messages — an unknown name is `unknown //servo: directive "pots"`, a bad
pattern gets net/http's own message. Grammar, placement and pattern syntax only: handler-shape and
cross-route checks stay with `generate`, which has the resolved module.

**A directive a space turned into a comment.** `// servo:get /orders` is prose to Go and to
`generate`; when the rest of the line is a well-formed route or config directive, the analyzer says
so — `this comment reads as //servo:get /orders, but the space after // makes it an ordinary
comment` — and skips generated files, whose headers quote directives.

**A capability-named method servo will never call.** `Run() error`, `Stop(ctx)` with no error
result, `Init()` without a context — on a type a constructor in the package returns — have a
[capability](lifecycle.md#the-seven-capabilities)'s name and not its signature, and capabilities are
detected by signature alone.

`servo generate` makes all three checks too; the analyzer runs them everywhere, including in
packages no injector has reached yet.

`Include` is the marker most worth having the analyzer for. A shared marker set lives in its own
package, away from any spec file, which is where the `servoinject` constraint is easiest to forget —
and `servo generate` [refuses an untagged included file](spec.md#include) only once some injector
reaches it.

### Using the analyzer from another tool

The analyzer is exported, as `Analyzer` in
[`github.com/okian/servo/v3/servovet`](https://pkg.go.dev/github.com/okian/servo/v3/servovet) — a
package with exactly that one identifier in it. `cmd/servo-vet` is a thin `singlechecker` wrapper
around the same variable, so anything that speaks `go vet`'s protocol — `go vet -vettool=`, most
editor integrations — works against the binary, and anything that wants an `*analysis.Analyzer`
imports the package. `singlechecker`, `multichecker`, `unitchecker` and `analysistest` all take it
directly.

`golangci-lint`'s module plugin system is the second kind. It needs a Go module that registers the
analyzer at init, which is why the variable had to leave `package main`:

```go
package servolint

import (
	"github.com/golangci/plugin-module-register/register"
	"github.com/okian/servo/v3/servovet"
	"golang.org/x/tools/go/analysis"
)

func init() {
	register.Plugin("servovet", func(any) (register.LinterPlugin, error) { return plugin{}, nil })
}

type plugin struct{}

func (plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{servovet.Analyzer}, nil
}

func (plugin) GetLoadMode() string { return register.LoadModeTypesInfo }
```

`LoadModeTypesInfo`, not `LoadModeSyntax`: both checks resolve identifiers through
`pass.TypesInfo` — a marker call is recognised by the `*types.Func` it resolves to, never by the
text `servo.Build` — so syntax alone is not enough. Name that module in a `.custom-gcl.yml` and
`golangci-lint custom` builds a linter binary with it compiled in.

## Wiring it into a project

**`go:generate`** — what `servo init` scaffolds, in its own untagged `servo_generate.go`, pinned to
your module's own servo version by the `tool` directive `go get -tool` wrote into `go.mod`:

```go
//go:generate go tool servo generate
```

Both halves are load-bearing, and both are covered under [`init`](#init): the directive cannot sit
in the tagged spec file, because `go generate` honours build constraints; and it cannot be `go run
github.com/okian/servo/v3/cmd/servo`, because the generator's dependencies are not in a consumer
module's build list.

If the injector is a [variant](#build-variants), `servo init --tags=prod` scaffolds it already
gated — `//go:build servoinject && prod`, with `--tags=prod` on the directive when it writes the
directive file — and names any existing spec that does not yet exclude the new tags, which is the
step everyone forgets.

**CI** — run `check`, not `generate`, so a stale file fails the build instead of being quietly
fixed on the runner. **A project with variants needs one `check` per configuration**, since each
variant has its own generated file and `check` only inspects the one its flags select:

```
servo check
servo check --tags=prod
```

[`examples/variants`](https://github.com/okian/servo/tree/master/examples/variants) is a working
two-variant project, checked both ways in this repository's own CI.
[servo in CI](../servo-in-ci.md) has a workflow to copy that does exactly that; this repository's
own `go.yml` runs `go run ./cmd/servo` because it is the servo repository, which a consumer's
module is not.

**Pre-commit** — [`githooks/pre-commit`](https://github.com/okian/servo/blob/master/githooks/pre-commit)
runs `go tool servo check` locally, in that form and for the same reason the `go:generate` line
uses it. It is not enabled by default; turn it on per clone with:

```
git config core.hooksPath githooks
```
