# Spec file and markers

**Who this is for:** anyone writing or changing the one file that tells servo what to build.

The spec file is the entire input you author. It declares the roots of the object graph and any
bindings that resolution can't work out on its own — and nothing else. It is read as syntax and
never executed, which drives almost every rule on this page.

## What a spec file is

```go
//go:build servoinject

package main

import (
	"example.com/servobasic/api"
	"example.com/servobasic/mockstore"
	"example.com/servobasic/postgres"
	"example.com/servobasic/relay"
	"example.com/servobasic/store"
	"example.com/servobasic/worker"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Root[*worker.Consumer](),
		servo.Root[*relay.Relay](),
		servo.Bind[store.Store, *postgres.DB](),
		servo.Override[store.Store, *mockstore.Store](),
	)
}
```

That is [`examples/basic`](https://github.com/okian/servo/blob/master/examples/basic/cmd/basic/spec.go)'s
real spec file, using three of the six markers. The other three —
[`Scoped`](#scoped), [`Value`](#value) and [`Include`](#include) — are on this page too.

Note what is *not* in it: the `//go:generate` directive. That lives in an untagged
`servo_generate.go` beside it, because `go generate` honours build constraints and would never
reach a directive inside a `//go:build servoinject` file. See
[the `go:generate` directive](#the-gogenerate-directive).

Four facts about it that matter:

**The file's directory decides where the output goes.** `servo generate` writes `servo_gen.go`
into the same directory as the spec file. Put the spec next to the `main.go` that will call the
generated `New`.

**The function name is a convention, nothing more.** Servo searches for a call to `servo.Build`; it
never looks for a function called `wire`. The name exists so a human reading the file knows why
it's there.

**`wire` is never called, by anything.** Nothing in your program references it, and the file never
reaches your binary — under the `servoinject` build configuration it is just an ordinary declared
function that nothing happens to call.

**Only the `Build` call is read.** The rest of the file can contain whatever you like. It has to be
valid Go that type-checks under the `servoinject` tag, but servo extracts nothing from it.

## The build tag

```go
//go:build servoinject
```

**This is required.** A spec file without it is a hard error:

```
spec.go:16:2: spec file is missing a `//go:build servoinject` constraint —
as written it would compile into the real binary
```

The reason is the markers themselves. Every one of them — `Build`, `Root`, `Bind`, `Override`,
`Scoped`, `Value`, `Include`, `Linger`, `Max` — `panic`s when executed. They exist to be *read*, and
a marker call that actually runs means generation was skipped or the tag was missing. Rather than
silently returning a nil app, they fail loudly. The build tag is what guarantees they never run:
`servo generate` loads your module with `-tags=servoinject`, so it sees the spec file, and an
ordinary `go build` doesn't.

The same requirement extends to any file [`Include`](#include) pulls markers from, even though that
file is not a spec file: it declares marker calls, so it would compile them into your binary.

The generated file carries the mirror-image constraint, so the two files are never in the same
build at once. That's also why generation never trips over its own previous output.

**Mirror image means your whole constraint, with `servoinject` negated** — not a fixed
`!servoinject`. A spec gated `//go:build servoinject && !prod` generates a file gated
`//go:build !servoinject && !prod`. Everything you wrote besides the tag survives untouched, which
is what lets one injector have several generated files that exclude each other: you express the
exclusion in your own spec files, and servo carries it across. See
[Build variants](cli.md#build-variants).

**The check is on meaning, not on text.** Servo parses the constraint and asks whether it can only
be satisfied when `servoinject` is set, evaluating every *other* tag as true. So
`//go:build servoinject && linux` passes (it still requires the tag), while
`//go:build servoinject || linux` does not (it can be satisfied on Linux without the tag). Both
`//go:build` and the legacy `// +build` syntax are recognised.

[`servo-vet`](cli.md#servo-vet) runs this identical check as an analyzer, so a missing tag surfaces
in your editor rather than at `go generate` time.

## Spec discovery

Servo scans the **main module only** for `servo.Build(...)` calls. Packages from dependencies are
loaded and scanned for candidate providers, but a `Build` call in one of them is not your injector
and is not treated as one.

**Several injectors are normal.** A monorepo whose `cmd/api`, `cmd/worker` and `cmd/migrator` each
wire their own graph has three injectors, and `generate`, `check` and `doctor` process all three in
one pass. Which commands do that and which ask you to pick one is in
[CLI commands](cli.md#--dir-and-injector-scope).

**Two `Build` calls in the same package is an error**, even across two files. That package can only
own one generated file, so a second call is genuinely ambiguous rather than a second injector:

```
servo: multiple servo.Build(...) calls found in the same package example.com/app/cmd/api
(ambiguous — which one owns the generated file?):
  cmd/api/spec.go:16:2
  cmd/api/extra.go:9:2
```

**No `Build` call anywhere is an error** pointing you at the fix:
``servo: no servo.Build(...) call found — run `servo init` to scaffold a spec file``.

## `Build`

```go
func Build(...Marker)
```

Declares the injector. Every argument must be a `Root`, `Bind`, `Override`, `Scoped`, `Value` or
`Include` call written inline — with explicit type arguments for the five generic ones, and with a
function *name* for `Include`.

That is a real constraint, not a style preference. Servo reads the type arguments out of the
type-checker's instantiation info for the call it found in the syntax tree. A marker value stored in
a variable, returned from a helper, or collected into a slice and spread has no instantiation at the
`Build` call site to read:

```go
// Fine.
servo.Build(
	servo.Root[*api.Server](),
)

// Not fine — nothing here for servo to read at the Build call.
markers := []servo.Marker{servo.Root[*api.Server]()}
servo.Build(markers...)
```

[`Include`](#include) is what the second form was reaching for, and it works because servo reads the
named function's body as syntax rather than taking a value the call site has already erased.

Anything else in the argument list is rejected with its position:
`servo.Build argument is not a marker call`, or
`servo.Build argument must be a Root/Bind/Override/Scoped call with explicit type arguments`.

`Marker` is an empty struct. It carries no data and exists only to give the markers a return type
that makes `Build`'s argument list type-check.

## `Root`

```go
func Root[T any]() Marker
```

Declares `T` as a root of the graph. The graph servo builds is exactly the transitive closure of
its roots: everything a root depends on, directly or indirectly, is constructed, and every other
candidate in your module is left out of the generated file entirely.

- **Several roots are normal.** An HTTP server, a background worker and a relay can be three
  independent roots that share a logger; the shared node is constructed once.
- **Roots are usually pointer types** (`*api.Server`), because that's what constructors usually
  return. `T` must match the provider's result type exactly — see
  [identity is by type](resolution.md#identity-is-by-type).
- **A root may be an interface.** It resolves the same way an interface parameter does, by
  structural search or an explicit `Bind`.
- **A root with no provider is a diagnostic**, reported against the `Root[]()` call site.

`servo why <type>` answers the inverse question — which root pulled a given node in.

## `Bind`

```go
func Bind[I, C any]() Marker
```

Declares that concrete type `C` is the implementation to use wherever interface `I` is requested.

You need it when a parameter's interface type has **more than one** implementation in your module,
which is otherwise an ambiguity diagnostic — and the diagnostic prints the exact `Bind` lines to
choose from. You don't need it when there's exactly one implementation; that auto-binds.

Two behaviours worth knowing:

**An explicit bind always wins**, even over an interface that would have resolved unambiguously on
its own. It is a deliberate override, not only a tie-breaker. `servo explain` reports the selected
provider's binding as `explicit bind`.

**A bind switches resolution to an exact-type lookup.** Once `I` is bound to `C`, servo looks for
the one function returning exactly `C` and never runs a structural search for `I`. If nothing
returns `C`, that's a missing-provider diagnostic for `C`, with no candidate suggestions — because
suggesting alternatives to an explicit instruction would be second-guessing it.

Two rules are enforced at declaration time:

| Rule | Error |
| --- | --- |
| The second type argument must be a concrete type | `servo.Bind's second type argument must be a concrete type, not an interface (…) — Bind/Override name the concrete implementation, they don't chain to another interface` |
| The same interface may be bound only once | `servo.Bind[…, ...] declared twice — first at <pos>` |

The second rule exists so a second `Bind` can't silently win over the first with no diagnostic at
all.

## `Override`

```go
func Override[I, C any]() Marker
```

Declares a test-only replacement for `I`. Overrides are ignored when generating production code
and applied — with priority over `Bind` — when generating the test variant.

Declaring both for the same interface is the intended pattern, not a collision:

```go
servo.Bind[store.Store, *postgres.DB](),        // production
servo.Override[store.Store, *mockstore.Store](), // tests
```

**One `Override` anywhere in the spec is what makes `servo generate` emit a second file**,
`servo_gen_test.go`, containing `NewTestApp` and `TestApp`. With no overrides declared, only
`servo_gen.go` is written. The generated test type and why it can't share `App` are covered in
[Generated API](generated-api.md#the-test-variant).

Same rules as `Bind`: the second type argument must be concrete, and the same interface may be
overridden only once. `Override` is checked against other `Override`s only, which is why pairing it
with a `Bind` for the same interface is fine.

An override applies to the **whole graph**, not to one consumer. There is no way to give one
component the mock and another the real implementation; see
[Limitations](../limitations.md#an-override-applies-everywhere).

## `Scoped`

```go
func Scoped[T, I any](...ScopeOption) Marker
```

Declares that `T` is a keyed, refcounted, lifecycle-managed instance rather than a singleton, and
that `I` — an interface in your own package — is what consumers depend on to reach it.

```go
servo.Build(
	servo.Root[*api.Server](),
	servo.Scoped[*chat.Room, chat.Rooms](
		servo.Linger(30*time.Second),
		servo.Max(10_000),
	),
)
```

`T` must declare a `ScopeKey` method; `I` must be a non-empty interface declaring only `Acquire`,
`Stats`, or both. Everything about what that means — the extractor's shape, which nodes end up in
the scope, when an instance is torn down — is on its own page:
[Scoped instances](scopes.md).

### `Linger` and `Max`

```go
func Linger(time.Duration) ScopeOption
func Max(int) ScopeOption
```

The only two `ScopeOption`s. Both are read as syntax like everything else here, so both arguments
have to be **constant expressions** — `30*time.Second` and `10_000` are fine, a package variable or
a function call is not.

Omitted, they take `servo.DefaultLinger` (30 seconds) and `servo.DefaultMax` (10,000). Neither is
legal outside a `Scoped` argument list, and putting one directly in `Build` says so.

Two `Scoped` declarations whose `ScopeKey` methods return the same key type share one registry, and
therefore one policy: declaring different `Linger` or `Max` values across them is an error, because
there is only one of each to set.

## `Value`

```go
func Value[T any]() Marker
```

Declares that `T` is supplied by the caller rather than built by a provider — a parsed flag set, a
version string injected at link time, a `*sql.DB` opened by a test harness, a fixed clock.

Everything else in the graph is resolved by finding the one function that produces it. A value that
only exists once the process is already running has no such function, and the workaround servo left
open — a package-level `var` in `main`, read back by a small provider beside it — is the
global-lookup pattern this version exists to remove.

```go
servo.Build(
	servo.Value[config.Flags](),
	servo.Root[*api.Server](),
)
```

**Declaring one changes the generated API additively.** The injector keeps `New(ctx)` and gains a
`Values` struct and a `NewWith` that takes it. What exactly gets emitted, including the test
variant's `TestValues`/`NewTestAppWith`, is in
[Generated API](generated-api.md#values-and-newwith).

Three rules, each of which follows from the marker being a declaration rather than a hint:

**A `Value` beats any provider that also produces `T`.** Declaring one is how you say "this comes
from the caller", which is only meaningful if it wins. It sits above the explicit-`Bind` step in
[selection precedence](resolution.md#selection-precedence), and no diagnostic is produced for the
provider it displaces — the spec file said so.

**A declared value nothing depends on is a diagnostic**, not a silently unused struct field:

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

The position is the `servo.Value[…]()` call site — the declaration servo is complaining about, not
the type.

Note that this is checked per generated file, so a value used only by a type an
[`Override`](#override) replaces is reported when the test variant is resolved.

**`T` is matched by type, exactly as a constructor parameter is.** The same identity rules apply:
`T` and `*T` are different keys, and declaring the same `T` twice is
`servo.Value[…]() declared twice — first at <pos>`.

A supplied value appears everywhere a constructed node does — `App.Graph()`, `servo graph`,
`servo explain`, `servo why` — at **level 0**, since the app has it before it builds anything, with
its binding reported as `supplied` and its provider as `the caller, via NewWith`:

```
$ servo explain --dir cmd/api conf.Flags
example.com/app/conf.Flags
  provider:     the caller, via NewWith (cmd/api/spec.go:13:3)
  binding:      supplied
  lifetime:     supplied — handed to NewWith once, held for the life of the process
  level:        0
  depends on:   none
  depended on:  *example.com/app/postgres.DB
  capabilities: none
```

That lifetime line is the limitation worth reading twice: a supplied value is one per app, handed
in at `NewWith` and held for the life of the process. It is not a per-call or per-request value.

## `Include`

```go
func Include(func() []Marker) Marker
```

Splices another function's marker list into this `Build` call. It exists for the module with three
binaries whose specs are identical below the transport: the shared markers are written once, and
adding a fourth `Bind` stops being a three-file edit nothing checks.

The shared set is an ordinary function in an ordinary package, carrying the same build tag a spec
file carries:

```go
//go:build servoinject

// internal/wiring/wiring.go
package wiring

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *postgres.DB](),
		servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(time.Minute)),
	}
}
```

Each spec then carries only what actually differs — here, its own transport's root:

```go
func wire() {
	servo.Build(
		servo.Include(wiring.Shared),
		servo.Root[*api.Server](),
	)
}
```

[`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial) is the worked
case: three injectors sharing one `internal/wiring/wiring.go`, and three spec files of nineteen
lines each where each used to be fifty-three — eleven marker calls per spec, ten of them
identical in all three.

**The argument names the function; it is never called.** It has to be a plain identifier or a
package selector resolving to a declared function — not a function literal, not a method value, not
a call. Anything else is
``servo.Include's argument must name a declared func() []servo.Marker — not a literal, a method value, or a call``.

**The included file must carry the `servoinject` build tag**, for exactly the reason a spec file
does: every marker it returns panics if executed, so an untagged file compiles them into your real
binary. Both failure modes have their own message — one for a file this build cannot see at all, one
for a file it can see that isn't gated:

```
cmd/api/spec.go:12:3: servo.Include names example.com/app/wiring.Shared, which is declared in a
file without a `//go:build servoinject` constraint — as written it would compile into the real
binary, where every marker it returns panics
```

[`servo-vet`](cli.md#servo-vet) flags the calls in that file directly, which is the faster loop; a
shared marker set lives away from any spec file, which is where the tag is easiest to forget.

**The body must be exactly one `return` of one slice literal of marker calls.** Its contents are
read as syntax by the same code that reads `Build`'s own argument list. Anything servo would have to
*execute* to know the answer — a variable, a conditional, an `append` — is refused:

```
wiring/wiring.go:10:1: Shared must be exactly `return []servo.Marker{ ...marker calls... }` —
its body is read as syntax and never run, so anything servo would have to execute to know the
answer is refused
```

**The function may live in another package**, which is the whole point: a shared marker set is not
part of any one injector. Servo finds the declaration by walking the injector package's import
graph, so the spec file importing it is what makes it reachable.

**Includes may nest.** An included function may itself `Include` another. A cycle is a diagnostic
naming the path that closed it, not a hang:

```
wiring/wiring.go:19:3: servo.Include cycle — Shared includes itself, through:
  example.com/app/wiring.Shared
  example.com/app/wiring.More
```

**Local declarations win.** Included markers are spliced in where the `Include` sits, so a `Bind` or
`Override` written after it in the spec file supersedes an included one for the same interface. That
is deliberate and is the only ordering that makes a shared set worth having: a shared default you
cannot deviate from in one binary is a shared default you stop using. Two *local* declarations for
the same interface are still the duplicate they always were —
`servo.Bind[…, ...] declared twice`.

## The `go:generate` directive

```go
// servo_generate.go — untagged, and holding only this
package main

//go:generate go tool servo generate
```

Scaffolded by `servo init` into its **own file**, not into the spec file, and worth keeping in that
form. Both halves are deliberate:

**Untagged, in a separate file.** `go generate` honours build constraints, so a directive inside the
`//go:build servoinject` spec file is invisible to `go generate ./...` — which then exits 0, prints
nothing, and generates nothing.

**`go tool servo`, not `go run <module path>`.** A module that requires servo requires it for the
marker package alone, so the generator's own dependencies are not in that module's build list and
`go run github.com/okian/servo/v3/cmd/servo` fails on a missing `go.sum` entry. `go get -tool
github.com/okian/servo/v3/cmd/servo`, once per module, puts the generator in `go.mod` — which also
pins its version, so every developer and every CI runner generates with the same one. That matters
because two servo versions can legitimately produce two different (both correct) files, and
`servo check` compares bytes.

## Errors from this stage

Everything the spec parser can reject, in one place:

| Message | Cause |
| --- | --- |
| `no servo.Build(...) call found` | No injector in the main module |
| `multiple servo.Build(...) calls found in the same package` | Two `Build` calls in one package |
| ``spec file is missing a `//go:build servoinject` constraint`` | Untagged spec file |
| `servo.Build argument is not a marker call` | Something other than a call in the argument list |
| `servo.Build argument must be a Root/Bind/Override/Scoped call with explicit type arguments` | A marker without inline type arguments |
| `unrecognized servo marker "X" inside Build(...)` | A `servo` function that isn't a marker |
| `servo.Root expects exactly one type argument` | `Root` with the wrong arity |
| `servo.Bind/Override expects exactly two type arguments` | `Bind`/`Override` with the wrong arity |
| `second type argument must be a concrete type, not an interface` | Binding an interface to an interface |
| `servo.Bind[…] declared twice` | Duplicate bind (or duplicate override) for one interface, both written locally |
| `servo.Value expects exactly one type argument` | `Value` with the wrong arity |
| `servo.Value[…]() declared twice` | One `Value` per type |
| `servo.Include takes exactly one argument, the name of a func() []servo.Marker` | `Include` with the wrong arity |
| `servo.Include's argument must name a declared func() []servo.Marker` | A literal, a method value, or a call where the function name belongs |
| `servo.Include names …, whose declaration is not in this build` | The included function is not reachable in this configuration |
| ``servo.Include names …, which is declared in a file without a `//go:build servoinject` constraint`` | The included file is untagged |
| ``X must be exactly `return []servo.Marker{ ... }` `` | The included function's body is something servo would have to run |
| `servo.Include cycle — X includes itself, through:` | Two included sets include each other |
| `servo.Scoped expects exactly two type arguments` | `Scoped` with the wrong arity |
| `servo.Scoped's first type argument must be the concrete scoped type` | An interface where the scoped type belongs |
| `servo.Scoped's first type argument must be a pointer, not X` | `Acquire` has to be able to return a zero alongside an error |
| `servo.Scoped's second type argument must be an interface` | A concrete type where the accessor interface belongs |
| `servo.Scoped's accessor interface … declares no methods` | `any` is satisfied by everything, which makes the accessor unusable |
| `servo.Scoped[T, …] declared twice` | One scoped type, one declaration |
| `servo.Scoped[…, I] declared twice` | One accessor interface cannot stand for two scoped types |
| `servo.Scoped's arguments must be servo.Linger(...) or servo.Max(...) calls` | Something else in the option list |
| `servo.X is not a scope option` | A `servo` marker that isn't `Linger` or `Max` |
| `servo.Linger/Max is a scope option, not a Build marker` | An option at the top level of `Build` |
| `servo.Linger/Max declared twice in the same servo.Scoped` | One value each per declaration |
| `servo.Linger/Max's argument must be a constant expression` | Read as syntax, never executed |
| `servo.Linger(…) must not be negative` | Use `servo.Linger(0)` for die-with-the-last-holder |
| `servo.Max(N) must be positive` | A scope that can hold no instances can never hand one out |
| `servo runtime package … is not imported by any file this build configuration can see` | No spec file exists yet, or every one of them is gated out of this configuration |

Diagnostics from the *later* stages — missing providers, ambiguity, cycles — are on the
[Diagnostics](diagnostics.md) page.
