# Resolution rules

**Who this is for:** anyone whose constructor isn't being picked up, whose dependency resolved to
the wrong thing, or who wants to know exactly what servo will accept before writing code against
it.

Resolution is the stage between "here is my module" and "here is the file to write". It has exactly
two outcomes: a complete, ordered construction plan, or a set of diagnostics. Never a partial
graph.

## Every node is a singleton, with one exception

Everything on this page describes how servo resolves **singletons**: one instance per type, built
once in `New`, held for the life of the process.

The exception is a [scope](scopes.md) — a keyed sub-graph whose members are built once per live key
instead. Scopes reuse every rule here (identity by type, provider shapes, selection precedence,
levelling) and add three of their own: the key type resolves to the extracted key rather than to a
provider, an accessor interface resolves to generated code rather than to a candidate, and a node
becomes scoped by transitively depending on either. The page above covers all three.

A [`servo.Value`](spec.md#value) is *not* an exception to the lifetime rule — it is one per app,
handed to `NewWith` and held for the life of the process, exactly like a constructed singleton. What
it changes is where the instance comes from: it is supplied rather than built, so it has no
provider, sits at level 0, and takes precedence over every rule below.

## Identity is by type

Every value in the graph is identified by one thing: the fully qualified string of its type.

```
*example.com/app/postgres.DB
example.com/app/store.Store
```

Consequences, all of which follow from that single rule:

- **`T` and `*T` are different nodes.** A constructor returning `*postgres.DB` does not satisfy a
  parameter of type `postgres.DB`.
- **Aliases collapse to their target.** `types.Unalias` is applied, and applied through pointer
  indirection too, so `*Alias` and `*Underlying` are the same key rather than two strings that
  merely look different.
- **Import paths are always written in full**, never abbreviated to a package name, so two
  same-named types from different packages can never collide.
- **A node is a singleton.** Each key resolves once; every consumer of that key gets the same
  instance. There is no per-request scope and no way to ask for a second one.
- **There are no names or tags.** Two constructors returning the same type are not two instances,
  they are an ambiguity. (`Key` carries a `Tag` field, but nothing in the public API sets one.) The
  fix is to make them two distinct *types* — see
  [Limitations](../limitations.md#there-are-no-tags-or-names-so-you-cant-have-two-of-the-same-type)
  and the README's [Multiple instances of the same type](https://github.com/okian/servo/blob/master/README.md#multiple-instances-of-the-same-type).

## What counts as a provider

A *candidate provider* is a function servo is willing to call to produce a value. To qualify, a
function must satisfy all of the following.

**It is a top-level function.** Methods are never providers, however constructor-shaped they look.
Neither are function literals, or functions returned by other functions.

**It lives in a non-test file.** `_test.go` files are not part of the scan at all, so a fake
defined in one can never be wired in. This is why every mocking integration puts its adapter in an
ordinary file — see the README's
[Mocking section](https://github.com/okian/servo/blob/master/README.md#mocking).

**It is exported**, unless it lives in the injector's own package — where unexported functions are
candidates too, so a small private provider can sit next to your `main.go`.

**It is neither generic nor variadic.** A generic function has no single result type to key on. A
variadic parameter's static type is a slice, and a slice can never be provided, so such a function
could never resolve anyway; both are rejected up front with a specific reason rather than
surfacing later as a confusing "no provider for `[]T`".

**Its results match one of four shapes**, and only these four:

```go
func New(deps...) T
func New(deps...) (T, error)
func New(deps...) (T, func())
func New(deps...) (T, func(), error)
```

`T` is the value being provided. The `func()` is a cleanup function, called last during that node's
shutdown ([Lifecycle](lifecycle.md#the-cleanup-func)). The `error` position must be the `error`
interface itself — a concrete type that merely *satisfies* `error` is rejected, with a reason
saying exactly that, because returning `*MyError` instead of `error` is a well-known Go trap and
guessing at intent here would be worse than refusing. The cleanup func must be exactly `func()`:
no parameters, no results, not variadic.

**`T` is a named type, a pointer to a named type, or a non-empty interface.** Everything else is
rejected: primitives, slices, arrays, maps, `any`, and pointers to unnamed types. There is no
opt-in mechanism to admit them.

That last rule is the one people meet first, and the reason for it is that a bare `string` or
`time.Duration` is an identity collision waiting to happen — every provider of a `string` in the
whole module would be the same node. Wrap configuration values in a named type.

**Functions with no results at all** are neither accepted nor rejected. They aren't attempting to
construct anything, and listing them would bury the real answers in `servo list --rejected` under
every helper function in the module.

## Rejection reasons

Every reason string `servo list --rejected` can print, with what to do about it:

| Reason | What happened | Fix |
| --- | --- | --- |
| `unexported, outside injector package` | Lowercase function in a package other than the injector's | Export it, or move it into the injector package |
| `generic function — unsupported` | The function has type parameters | Write a concrete wrapper that instantiates it |
| `variadic parameter — unsupported` | A `...T` parameter, e.g. an options pattern | Wrap: `func New() *Client { return newClient(opts...) }` |
| `does not match a supported result shape` | Results aren't one of the four shapes above | Reduce to one value, plus optionally a cleanup func and `error` |
| `second result is X, which implements error but is not the error interface itself` | Returned `*MyError` rather than `error` | Return `error` |
| `third result is X, which implements error but is not the error interface itself` | Same, in the `(T, func(), error)` shape | Return `error` |
| `result type is a primitive (string)` | Returns a bare `string`, `int`, `bool`, … | Wrap it in a named type |
| `result type is a slice` / `is an array` / `is a map` | Returns `[]T`, `[N]T`, `map[K]V` | Wrap it in a named struct type |
| `result type is any (empty interface)` | Returns `any`/`interface{}` | Return a concrete type or a real interface |
| `result type is a pointer to an unnamed type (*struct{…})` | Returns a pointer to an anonymous struct | Name the type |
| `result type is not a named type, pointer-to-named, or interface` | Anything else — a channel, a func type, … | Wrap it |
| `method, not a function` | An exported method with at least one result | Add a package-level constructor |

`method, not a function` will be the bulk of the output on any real project, because every
`Init`/`Stop`/`Health` method in your module lands here. That's deliberate: it means the answer to
"why wasn't my `Get` method used as a provider" is present rather than absent.

## Where candidates come from

The scan covers **every loaded package** — your module, its dependencies, and the standard library
— because a type-checking session that stopped at your module boundary couldn't answer
`types.Implements` questions across it.

Two different scopes then apply, and the difference matters:

| Lookup | Scope |
| --- | --- |
| Exact type match (a concrete parameter) | Every loaded package, including stdlib and third-party |
| Structural interface search (an interface parameter) | The **main module** only |

Restricting the structural search is a guard against deep third-party dependency trees producing
false ambiguity at scale. It is deliberately the whole main module rather than "packages the
consumer imports": an implementation living in a package the consumer doesn't import is the entire
point of depending on an interface.

**The exact-type lookup being unscoped has a real consequence.** Depend directly on a third-party
concrete type and its own constructors become the candidates — including cases you can't fix by
deleting one:

```
$ servo generate
servo: no provider for *database/sql.DB
  needed by *example.com/probe/app.Server  app/app.go:7:6
  root                                     spec.go:12:3

  2 functions produce *database/sql.DB — remove or rename all but one:
      sql.OpenDB      database/sql/sql.go:837:6
      sql.Open        database/sql/sql.go:868:6
```

You cannot remove `sql.Open`. The fix is to stop asking the graph for a foreign type: write your
own thin wrapper (`servo new adapter postgres` scaffolds one), give it a constructor with the
configuration it needs, and depend on `*postgres.DB` instead. That indirection is a good idea for
its own reasons anyway — it's where retries, tracing and connection settings live.

## Selection precedence

For each requested key, in this order. The first step that yields exactly one provider wins.

**0. A declared `servo.Value`.** If the key is one, resolution stops there: the value comes from the
caller, and no provider is consulted. This is above `Bind`, not beside it — a
[`servo.Value`](spec.md#value) beats *any* provider that also produces the type, including one an
explicit `Bind` names, and displacing that provider is not a diagnostic. Declaring the marker is how
you say "this comes from the caller", which is only meaningful if it wins. (The same step also
resolves a scope's key type and a scope's accessor interface, for the same reason: both are
declared, not searched for. See [Scoped instances](scopes.md).)

**0½. A `//servo:config` type.** A key carrying the
[config directive](config.md) resolves to its generated `ServoConfig` loader, ahead of every
provider — which is also why a hand-written constructor for such a type is a diagnostic rather
than a silent loser. Being below `Value`, a `servo.Value` for a config type still wins: "the
caller supplies this" outranks "the generated loader builds this", and the loader is then not
consulted and not generated for that graph.

**1. An explicit `Bind` or `Override`.** If the key has one, the request is *redirected* to the
named concrete type, and resolution continues from step 2 looking for that type instead.
`Override` beats `Bind` for the same key, and only applies when generating the test variant.

**2. Exact type match.** Look for functions returning exactly this type.

- **One** → selected. Binding is reported as `explicit bind` if step 1 redirected here, otherwise
  `sole candidate`.
- **More than one** → ambiguity diagnostic:
  `N functions produce X — remove or rename all but one`, listing each with its position. No
  `Bind` can resolve this, because there's no interface involved; make the instances distinct
  types.
- **Zero** → if step 1 redirected here, that's a missing-provider diagnostic for the bound type,
  with no suggestions (servo won't second-guess an explicit instruction). Otherwise continue to
  step 3.

**3. Structural interface search.** Only if the requested type is a non-empty interface. Every
main-module candidate whose result type satisfies it — via `types.Implements`, at generation time,
never a runtime assertion — is collected.

- **One** → selected, binding reported as `sole implementation`. This is the auto-binding that
  makes most interface dependencies need no configuration at all.
- **More than one** → ambiguity diagnostic listing the exact `servo.Bind[…]` line to add for each.
- **Zero** → missing-provider diagnostic.

Nothing here depends on how the *rest* of the graph is written. A dependency's declared type
decides how it resolves: declare an interface where a component should accept any of several
implementations, a concrete type where there's exactly one. Servo follows whichever you wrote and
never converts one into the other.

The one exception to "nothing else influences this" is step 0, and it is explicit by construction:
a marker in the spec file, written by you, is the only thing that can pre-empt the search.

Then the same three steps run for each of the selected provider's own parameters, recursively,
until the graph closes or a diagnostic is produced.

## Construction order and levels

Two different orderings come out of resolution, and they answer different questions.

**Construction order** is depth-first post-order: a node is placed after everything it depends on.
The generated `New` calls constructors in exactly this order, sequentially. It is a topological
order, so every dependency exists before the thing that needs it.

**Level** is `1 + max(level of dependencies)`, so leaves are level 1 and roots sit at the top:

```
── Level 1 ──  *logger.Logger, *queue.OrdersAccount, *queue.AuditAccount
── Level 2 ──  *postgres.DB, *worker.Consumer, *relay.Relay
── Level 3 ──  *api.Server
```

Levels exist for one purpose: **Init concurrency.** Nodes in the same level have no dependency
between them, so their `Init` calls can run concurrently. Construction itself is never
concurrent — it's cheap, and sequential construction keeps the generated code readable. See
[Lifecycle](lifecycle.md#init).

**Reachability.** The graph is the transitive closure of the declared roots. A perfectly good
candidate that nothing reaches is not an error and not a warning — it simply isn't in the generated
file. `servo why <type>` reports which root pulled a node in; `servo list` shows everything that
was a candidate, reachable or not.

## Scoped nodes and levels

A scoped node's level is counted from its own scope's floor, not the app's. `*chat.RoomLog` sits at
scope level 1 even when the `*logger.Logger` it borrows is at app level 4 — the scope's `Init`
phases don't depend on how deep the singletons it borrows happen to be. `servo explain` reports
the scope-relative number for a scoped node and the app-relative one for a singleton, and says
which it is on the `lifetime:` line.

## Cycles

A dependency cycle is always a build failure, reported with the full loop:

```
servo: dependency cycle detected
  *cycle.A cycle/graph.go:8:6
  *cycle.B cycle/graph.go:12:6
  *cycle.A cycle/graph.go:8:6  (cycle closes here, back to the first line)
```

There is no lazy-injection escape hatch, no provider proxy, and no way to break a cycle with
configuration. Constructor injection can't express one: `A` needs a finished `B` and `B` needs a
finished `A`. Break it by extracting the shared piece into a third type both depend on, or by
having one side hold a callback the other registers after construction.

## Determinism

The same source produces the same generated bytes, which is what makes `servo check` a meaningful
CI gate:

- The candidate index is sorted by source position (file, then line, then column), so package load
  order can't perturb it.
- Ambiguity candidate lists are sorted the same way.
- Positions embedded in the generated file are rewritten relative to the module root, so two
  checkouts at different absolute paths produce identical files. `servo graph` applies the same
  rewriting, because its output has to match the generated `App.Graph()` exactly; `explain`, `why`
  and `list` print absolute paths, which are for you and your editor rather than for a diff.

One caveat: two different servo versions may legitimately produce two different, both correct,
files. Pin one for the whole project with `go get -tool github.com/okian/servo/v3/cmd/servo`, which
records the generator's version in `go.mod` — the `go:generate` directive then just says
`go tool servo generate`. `servo check`'s stale report names the version it is running for exactly
this case.
