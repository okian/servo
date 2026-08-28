# Spec file and markers

**Who this is for:** anyone writing or changing the one file that tells servo what to build.

The spec file is the entire input you author. It declares the roots of the object graph and any
bindings that resolution can't work out on its own — and nothing else. It is read as syntax and
never executed, which drives almost every rule on this page.

## What a spec file is

```go
//go:build servoinject

package main

//go:generate go run github.com/okian/servo/v3/cmd/servo generate

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
real spec file, and it uses every marker there is.

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

The reason is the markers themselves. `Build`, `Root`, `Bind` and `Override` all `panic` when
executed — they exist to be *read*, and a marker call that actually runs means generation was
skipped or the tag was missing. Rather than silently returning a nil app, they fail loudly. The
build tag is what guarantees they never run: `servo generate` loads your module with
`-tags=servoinject`, so it sees the spec file, and an ordinary `go build` doesn't.

The generated file carries the mirror-image constraint, `//go:build !servoinject`, so the two files
are never in the same build at once. That's also why generation never trips over its own previous
output.

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

Declares the injector. Every argument must be a `Root`, `Bind`, `Override` or `Scoped` call
written inline with explicit type arguments.

That last part is a real constraint, not a style preference. Servo reads the type arguments out of
the type-checker's instantiation info for the call it found in the syntax tree. A marker value
stored in a variable, returned from a helper, or collected into a slice and spread has no
instantiation at the `Build` call site to read:

```go
// Fine.
servo.Build(
	servo.Root[*api.Server](),
)

// Not fine — nothing here for servo to read at the Build call.
markers := []servo.Marker{servo.Root[*api.Server]()}
servo.Build(markers...)
```

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

## The `go:generate` directive

```go
//go:generate go run github.com/okian/servo/v3/cmd/servo generate
```

Scaffolded by `servo init`, and worth keeping in the form above. `go run` against the module path
uses the servo version your `go.mod` already requires, so every developer and every CI runner
generates with the same version — which matters, because two servo versions can legitimately
produce two different (both correct) files, and `servo check` compares bytes.

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
| `servo.Bind[…] declared twice` | Duplicate bind (or duplicate override) for one interface |
| `servo.Scoped expects exactly two type arguments` | `Scoped` with the wrong arity |
| `servo.Scoped's first type argument must be the concrete scoped type` | An interface where the scoped type belongs |
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
| `servo runtime package … not found among loaded packages` | The spec file doesn't import `servo` |

Diagnostics from the *later* stages — missing providers, ambiguity, cycles — are on the
[Diagnostics](diagnostics.md) page.
