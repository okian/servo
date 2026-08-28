# Diagnostics

**Who this is for:** anyone looking at a message servo just printed and wanting to know what to
change.

Servo's position on failure is that a missing dependency, an ambiguous binding, or a cycle is a
build error with a source position — never a runtime panic and never a guess. This page lists every
message it can produce, grouped by the stage that produces it.

All of them exit 1. All of them go to stderr.

## Reproducing them yourself

[`examples/diagnostics`](https://github.com/okian/servo/tree/master/examples/diagnostics) is seven
small, permanently broken fixtures — one per failure mode — each runnable on its own:

```
go run ./cmd/servo generate --dir examples/diagnostics/missing
go run ./cmd/servo generate --dir examples/diagnostics/ambiguous
go run ./cmd/servo generate --dir examples/diagnostics/cycle
go run ./cmd/servo generate --dir examples/diagnostics/widening
go run ./cmd/servo generate --dir examples/diagnostics/crossscope
go run ./cmd/servo generate --dir examples/diagnostics/extractor
go run ./cmd/servo generate --dir examples/diagnostics/undeclared
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

Nothing in the graph can produce that type. With no candidate list following, it means servo found
**zero** possibilities — not several.

In order of likelihood:

1. **The constructor exists but wasn't accepted.** Run `servo list --rejected` and look for it. Every
   rejection reason is explained in
   [Resolution rules](resolution.md#rejection-reasons). Being unexported, generic, variadic, a
   method, or returning an unsupported shape all land here.
2. **The constructor is in a `_test.go` file.** Test files are not scanned at all.
3. **The type is slightly off.** `T` and `*T` are different nodes; a constructor returning
   `postgres.DB` will not satisfy a parameter of `*postgres.DB`.
4. **It's an interface with no implementation in the main module.** The structural search covers your
   own module only.
5. **An explicit `Bind` names a type nothing produces.** Then the reported type is the *bound* type,
   and no suggestions are printed — servo won't second-guess an explicit instruction.

### Ambiguous interface

```
ambiguous/store.go:23:6: servo: no provider for ambiguous.Store
  needed by *ambiguous.Server  ambiguous/store.go:23:6
  root                         ambiguous/spec.go:9:3

  2 types implement ambiguous.Store — add one of:
      servo.Bind[ambiguous.Store, *ambiguous.Postgres]()      ambiguous/store.go:11:6
      servo.Bind[ambiguous.Store, *ambiguous.Redis]()      ambiguous/store.go:17:6
```

Several types implement the interface, so auto-binding can't pick one. **The fix is printed
verbatim**: copy one of those lines into your `servo.Build(...)` call.

Candidates are sorted by source position, so the list is stable across runs. If a listed candidate
surprises you — a fake, a second implementation you forgot about — that's information: it is a real
type in your module that really does implement the interface.

### Ambiguous concrete type

```
servo: no provider for *sqsaccounts.Client
  2 functions produce *sqsaccounts.Client — remove or rename all but one:
      processor.NewClientA   processor/naive.go:5:6
      processor.NewClientB   processor/naive.go:6:6
```

Two functions return the *same* concrete type, with no interface involved. **No `Bind` can fix
this** — there's nothing to disambiguate between, because identity in the graph is purely by type,
and two functions returning one type are not two instances but one contested node.

The fix is to make them distinct types:

```go
type OrdersAccount struct{ *Client }
type AuditAccount  struct{ *Client }
```

Embedding keeps every method available with no delegation boilerplate. The README's
[Multiple instances of the same type](https://github.com/okian/servo/blob/master/README.md#multiple-instances-of-the-same-type)
walks through a complete worked example, and
[Limitations](../limitations.md#there-are-no-tags-or-names-so-you-cant-have-two-of-the-same-type)
explains why there are no tags to solve it with.

**A special case you can't fix by deleting a function:** ask for a stdlib or third-party concrete
type and its own constructors become the candidates.

```
servo: no provider for *database/sql.DB
  2 functions produce *database/sql.DB — remove or rename all but one:
      sql.OpenDB      database/sql/sql.go:837:6
      sql.Open        database/sql/sql.go:868:6
```

Write your own wrapper type and depend on that (`servo new adapter postgres` scaffolds one).

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

## Spec file

Full explanations on [Spec file and markers](spec.md#errors-from-this-stage). In short:

| Message | Fix |
| --- | --- |
| `servo: no servo.Build(...) call found` | Run `servo init` |
| `multiple servo.Build(...) calls found in the same package` | Delete one; a package owns one generated file |
| ``spec file is missing a `//go:build servoinject` constraint`` | Add the tag |
| `servo.Build argument is not a marker call` | Only `Root`/`Bind`/`Override` calls belong in `Build` |
| `servo.Build argument must be a Root/Bind/Override call with explicit type arguments` | Write markers inline, not via a variable or a helper |
| `unrecognized servo marker "X" inside Build(...)` | Not a marker function |
| `servo.Root expects exactly one type argument` | `Root[T]()` |
| `servo.Bind/Override expects exactly two type arguments` | `Bind[I, C]()` |
| `second type argument must be a concrete type, not an interface` | Name the implementation, not another interface |
| `servo.Bind[…] declared twice` | One bind per interface |

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
reason to generate, not a reason to refuse.

### Missing runtime package

```
load: servo runtime package github.com/okian/servo/v3/servo not found among loaded packages
(the spec file must import it to call Build/Root/Bind)
```

The spec file must import `github.com/okian/servo/v3/servo`. Usually this means the file was moved
outside the module being scanned, or `--dir` points somewhere with no spec at all.

### Multiple injectors in scope

```
servo: multiple injectors found in this scope — pass --dir to pick one:
  cmd/basic/spec.go:16:2
  cmd/migrator/spec.go:11:2
```

From `graph`, `explain`, `why` or `list` — commands that answer a question about *one* graph. Point
`--dir` at one injector's own directory. `generate`, `check` and `doctor` process all of them and
never print this.

## Scopes

These four are what a hand-written registry beside servo cannot give you. All are `generate`
failures; widening and cross-scope carry the same needed-by chain a resolution diagnostic does,
while the extractor-cycle and undeclared-scope messages name the two positions involved instead —
there is no consumer chain to walk for either. See [Scoped instances](scopes.md) for the feature
they belong to.

They are the four *named* scope diagnostics. A dozen and a half narrower ones — a stray scope key,
a node two scopes both claim, a scoped type declared as a root, a bound accessor — are tabulated
under [Other scope errors](#other-scope-errors) below.

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
  ScopeKey            extractor/session.go:36:17
  *extractor.Decoder  extractor/session.go:25:6

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
  ScopeKey  undeclared/tenant.go:22:16
  provider  undeclared/tenant.go:20:6

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

### `check`

```
servo check: cmd/api/servo_gen.go is stale — run `servo generate`
--- cmd/api/servo_gen.go (committed)
+++ cmd/api/servo_gen.go (fresh)
-	server := api.New(db)
+	server := api.New(db, cache)
```

The committed file doesn't match a fresh generation, with a `+`/`-` diff. Run `servo generate` and
commit the result.

```
servo check: cmd/api/servo_gen.go does not exist — run `servo generate`
```

The file was never generated, or was deleted. Same fix, plus commit it — see
[`doctor`](cli.md#doctor).

If `check` fails in CI but passes locally, the usual cause is two different servo versions. Pin it in
your `go:generate` directive.

### `doctor`

```
servo doctor: problems found
```

Printed after the report when any line was `[FAIL]`. Read the report — each line names its own
problem. `[WARN]` lines never cause this.

### `explain` and `why`

| Message | Cause |
| --- | --- |
| `servo: no node matches "X"` | No exact match and no type string *ending* with `X`. A leading `*` never suffix-matches: write `api.Server`, not `*api.Server` |
| `servo: "X" matches multiple nodes, be more specific: …` | Ambiguous suffix. The candidates are listed; pick one |
| `servo why: X is not reachable from any root` | The node resolved but no root depends on it |
| `usage: servo explain [--json] <type>` | Zero or several positional arguments. Flags must come *before* the type |
| `usage: servo why [--json] <type>` | The same, for `why` |

### Others

| Message | Cause |
| --- | --- |
| `servo: unknown command "X"` | Typo, or a flag placed before the command name |
| `servo graph: unknown --format "X" (want text\|json\|dot\|mermaid)` | Unsupported format |
| `servo init: <path> already exists` | `init` never overwrites |
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

The two things [`servo-vet`](cli.md#servo-vet) reports, and the two mistakes the compiler cannot
catch on its own. The generator makes both checks too; the analyzer catches them anywhere, in your
editor, before generation runs at all — including in packages no injector has reached yet.

## Runtime reports are not diagnostics

One distinction worth keeping clear. Everything above happens at build time. At run time, servo
reports rather than diagnoses: `Shutdown` returns a [`servo.Report`](servo-package.md#report), and a
line like

```
*api.Server: abandoned: context deadline exceeded
```

is not a servo error — it's servo telling you a component of yours didn't stop within its budget.
[Lifecycle](lifecycle.md#the-stop-budget) covers what to do about it.
