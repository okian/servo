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

The other non-singleton shape is an [extracted handler parameter](http.md): like a scope key, the
value exists once per request, produced by an extractor's `Extract` method rather than by a
provider. The extractor itself is an ordinary singleton node; what it produces is
never resolved from the graph, and a type with both a provider and an extractor is a generate-time
error. Both the extractor's `Extract(r *http.Request) (V, error)` and a middleware's
`Middleware(next http.Handler) http.Handler` are validated **by name**, the way `ScopeKey` is —
the result type varies per extractor, so no single interface could match it — where the seven
[capability interfaces](lifecycle.md#the-seven-capabilities) are found structurally, by
`types.Implements`.

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
- **Aliases collapse to their target, at any depth.** An alias is resolved wherever it appears —
  the type itself, a pointer's element, a type argument, a slice, map or channel element, a
  function's parameters and results, a struct's fields — so `*Alias` and `*Underlying`, or
  `*Cache[DBAlias]` and `*Cache[*DB]`, are one key rather than two strings that merely look
  different. An embedded field is the exception, because it is named after the type written for
  it: `struct{ Alias }` and `struct{ Target }` are different types.
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

**It is not in some other `package main`.** Nothing can import a main package, so a constructor
beside a sibling binary's `main.go` is reachable only from that package's own injector; for every
other injector it is rejected rather than selected into a generated file `go build` would then
refuse. The same rule applies to a [`//servo:` handler](http.md) declared in a main package.

**It is neither generic nor variadic.** A generic function has no single result type to key on. A
variadic parameter's static type is a slice, and a slice can never be provided, so such a function
could never resolve anyway; both are rejected up front with a specific reason, and that reason is
what a "no provider" diagnostic for the type the function would have returned prints — see
[near misses](#near-misses) below.

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

### The injector has to be able to refer to it

Everything above is about the function alone, and holds for every injector. One more rule depends
on which injector is asking: the generated file lives in the injector's package, calls the
constructor there, and holds the result in a field of the `App`, so both have to be things that
package can name. A candidate is set aside for an injector when:

- **it is under another tree's `internal/` directory** — Go lets only the tree rooted at the
  `internal` element's parent import it, and the same goes for a package its result type is
  declared in;
- **its result type is unexported in another package** — `func NewEvictor() *evictor` is fine Go,
  and a caller can hold the value, but the `App` field holding it cannot be declared outside
  package `cache`;
- **its package imports the injector's package**, directly or through others — an adapter that
  imports the core package it serves, with the spec file in that core package; importing it back
  would be a cycle.

Such a candidate is still in the index (`servo list` shows it, since the index is the same for
every injector), but resolution never selects it for an injector that cannot use it. It drops out
of structural search, so it cannot make an ambiguity of an implementation that is usable; an exact
or `Bind` request that it alone would have answered is reported as `no usable provider for …`,
with the reason. Parameter types are deliberately not judged: an argument is passed as a value the
generated code already holds, so its type is never written down. Each of these used to end in a
file the compiler refused, reported by `servo generate` as a bug in servo.

## Rejection reasons

Every reason string `servo list --rejected` can print, with what to do about it:

| Reason | What happened | Fix |
| --- | --- | --- |
| `unexported, outside injector package` | Lowercase function in a package other than the injector's | Export it, or move it into the injector package |
| `declared in another package main, which nothing can import` | A constructor in a sibling binary's main package | Move it to an importable package; only that package's own injector can use it where it is |
| `generic function — unsupported` | The function has type parameters | Write a concrete wrapper that instantiates it |
| `variadic parameter — unsupported` | A `...T` parameter, e.g. an options pattern | Wrap: `func New() *Client { return newClient(opts...) }` |
| `does not match a supported result shape: the error comes last — return (T, error)` | `(error, T)` | Swap the results |
| `does not match a supported result shape: the cleanup func comes before the error — return (T, func(), error)` | `(T, error, func())` | Swap the last two |
| `does not match a supported result shape: the second result is X, and must be error or a func() cleanup` | Two results, the second neither | Reduce to one value, plus optionally a cleanup func and `error` |
| `does not match a supported result shape: three results must be (T, func(), error)` / `N results — …` | Any other count or order | The same |
| `second result is X, which implements error but is not the error interface itself` | Returned `*MyError` rather than `error` | Return `error` |
| `third result is X, which implements error but is not the error interface itself` | Same, in the `(T, func(), error)` shape | Return `error` |
| `result type is a primitive (string)` | Returns a bare `string`, `int`, `bool`, … | Wrap it in a named type |
| `result type is a slice` / `is an array` / `is a map` | Returns `[]T`, `[N]T`, `map[K]V` | Wrap it in a named struct type |
| `result type is any (empty interface)` | Returns `any`/`interface{}` | Return a concrete type or a real interface |
| `result type is a pointer to an unnamed type (*struct{…})` | Returns a pointer to an anonymous struct | Name the type |
| `result type is not a named type, pointer-to-named, or interface` | Anything else — a channel, a func type, … | Wrap it |
| `method, not a function` | An exported method with at least one result | Add a package-level constructor |

`method, not a function` would be the bulk of the output on any real project, because every
`Init`/`Stop`/`Health` method in your module lands here, so the text listing groups methods by
receiver after the functions — one line per type — instead of repeating the reason per method. The
answer to "why wasn't my `Get` method used as a provider" is still present rather than absent, and
`--json` keeps one entry per method.

`servo list --rejected` also lists, under `candidates <injector> cannot use:`, the accepted
candidates the injector [cannot refer to](#the-injector-has-to-be-able-to-refer-to-it), each with
its reason: `it is declared in …, which … cannot import: it is internal to …`, `its result type … is
unexported, so the App generated in … cannot name it`, or `… which imports …, the injector's own
package, so importing it back from there would be an import cycle`.

### Near misses

When nothing provides a type, the diagnostic ends with whatever came close, one line each, with
the position to go to:

```
servo: no provider for *example.com/app/a.Client
  needed by *example.com/app/a.Server  a/a.go:17:6
  root                                 cmd/app/spec.go:12:3

  rejected: a.NewClient (a/a.go:7:6) — variadic parameter — unsupported
```

- `rejected:` — a main-module function the scan refused that would have returned the type (or,
  for an interface, a type implementing it), with the reason from the table above. A generic
  constructor counts when the type is one of its instantiations, and a package-level variable of
  function type (`var NewClient = newClient`) is named too: it reads like a constructor and has
  never been one.
- `unusable:` — a candidate the injector cannot refer to, with the reason.
- `near miss:` — a provider of the type's other half: `T` where `*T` was asked for or the reverse
  (named wherever it lives, so asking for `slog.Logger` points at `slog.New`), or, for an
  interface, a main-module type whose pointer has the methods: `returns memory.Store, which does
  not implement store.Store: Get has a pointer receiver, so only *memory.Store does`.
- `note:` — the type itself can never be provided: a slice, a map, a primitive, `any`.

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
point of depending on an interface. And it is the whole module whichever directory servo runs from
— `--dir` [only picks the injectors](cli.md#--dir-and-injector-scope), so a `go generate` run from
an injector's own directory searches exactly what a run from the module root does.

"The main module" is the module `--dir` is in. A module that a `go.work` lists beside it is not
part of it unless you pass [`--scope=workspace`](cli.md#--scope): by default its implementations
are as invisible to the search as any other dependency's, which is what a `GOWORK=off` build — CI's
usual one — would make of them anyway.

**The exact-type lookup covers every package, and prefers yours.** Depend directly on a stdlib or
third-party concrete type — `*slog.Logger`, `*sql.DB`, `*http.ServeMux` — and that package's own
constructors are candidates too. Your module's constructors decide: one `NewLogger() *slog.Logger`
of your own is selected over `slog.New` and `slog.Default`, and the outside ones are consulted only
when your module has none. A lone outside constructor is then selected, as it always was; several
are an ambiguity — but not one that asks you to delete a function you do not own:

```
$ servo generate
servo: ambiguous provider for *database/sql.DB
  needed by *example.com/probe/app.Server  app/app.go:7:6
  root                                     spec.go:12:3

  2 functions outside this module produce *sql.DB, and servo will not pick one of
  them for you:
      sql.OpenDB  database/sql/sql.go:837:6
      sql.Open    database/sql/sql.go:868:6

  Write its provider in this module — a constructor of your own is preferred
  over all of these:

	func NewDB(...) *sql.DB

  or declare servo.Value[*sql.DB]() and hand one to NewWith.
  with these imports in the spec file:
      "database/sql"
```

Writing that constructor is the usual answer, and wrapping the foreign type in one of your own
(`servo new adapter postgres` scaffolds one) is often the better one: it's where retries, tracing
and connection settings live. A `context.Context` parameter never reaches this lookup at all — it
is the [construction context](#selection-precedence).

## Selection precedence

For each requested key, in this order. The first step that yields exactly one provider wins.

**0. A declared `servo.Value`.** If the key is one, resolution stops there: the value comes from the
caller, and no provider is consulted. This is above `Bind`, not beside it — a
[`servo.Value`](spec.md#value) beats *any* provider that also produces the type, including one an
explicit `Bind` names, and displacing that provider is not a diagnostic. Declaring the marker is how
you say "this comes from the caller", which is only meaningful if it wins. (The same step also
resolves a scope's key type and a scope's accessor interface, for the same reason: both are
declared, not searched for. See [Scoped instances](scopes.md).)

**0¼. `context.Context`: the construction context.** A constructor parameter of type
`context.Context` receives the context `New`, `NewWith` or `NewTestApp` was called with — supplied
by the caller, like a value, and never searched for (the search used to find ten functions in the
standard library and call it an ambiguity). Use it for work done while constructing — a dial, a
ping — not for anything that outlives the call: `Run`'s context is the one that lasts. A declared
`servo.Value[context.Context]()` still wins, and so does a `Bind` or `Override` naming the type;
both are the spec file speaking. Only constructors `New` calls can take it: a
[scoped](scopes.md) constructor runs inside `Acquire`, after `New` has returned, and is refused
with a pointer to `Init(ctx)` instead; so is a context.Context among a `ScopeKey` extractor's or a
`//servo:` handler's later parameters, whose first parameter is already the context that matters.

**0½. A `//servo:config` type.** A key carrying the
[config directive](config.md) resolves to its generated `ServoConfig` loader, ahead of every
provider — which is also why a hand-written constructor for such a type is a diagnostic rather
than a silent loser. Being below `Value`, a `servo.Value` for a config type still wins: "the
caller supplies this" outranks "the generated loader builds this", and the loader is then not
consulted and not generated for that graph.

**1. An explicit `Bind` or `Override`.** If the key has one, the request is *redirected* to the
named concrete type, and resolution continues from step 2 looking for that type instead.
`Override` beats `Bind` for the same key, and only applies when generating the test variant.

**2. Exact type match.** Look for functions returning exactly this type — leaving out any the
injector [cannot refer to](#the-injector-has-to-be-able-to-refer-to-it) — and count your module's
first.

- **One of your module's** → selected. Binding is reported as `explicit bind` if step 1 redirected
  here, otherwise `sole candidate`. Constructors outside the module are not consulted.
- **More than one of your module's** → `ambiguous provider for X`: `N functions produce X, and
  servo tells values apart by type alone`, listing each with its position. No `Bind` can resolve
  this, because there's no interface involved, and renaming a function changes nothing: make the
  instances distinct named types — the message shows one.
- **None of yours, exactly one outside** → selected, as before.
- **None of yours, several outside** → if step 1 redirected here, or the type is not an interface,
  `ambiguous provider for X` naming them, with the two things you can do: write a constructor in
  your module, or declare a `servo.Value`. For an interface, step 3 runs first, and this is
  reported only if it finds nothing.
- **Zero** → if step 1 redirected here, a missing-provider diagnostic for the bound type: no
  alternative types are suggested (servo won't second-guess an explicit instruction), but the
  [near misses](#near-misses) say why nothing provides it — and when only candidates the injector
  cannot use produce it, the heading is `no usable provider for X`, with each one's reason.
  Otherwise continue to step 3.

**3. Structural interface search.** Only if the requested type is a non-empty interface. Every
main-module candidate whose result type satisfies it — via `types.Implements`, at generation time,
never a runtime assertion — is collected, apart from the ones the injector cannot refer to.

- **One** → selected, binding reported as `sole implementation`. This is the auto-binding that
  makes most interface dependencies need no configuration at all.
- **More than one** → `ambiguous provider for I`, listing the `servo.Bind[…]` line to add for
  each, written the way the spec file writes types — `servo.Bind[store.Store, *memory.Store]()`,
  bare for the spec's own package — followed by the imports the spec file still needs.
- **Zero** → missing-provider diagnostic, ending with the near misses.

Nothing here depends on how the *rest* of the graph is written. A dependency's declared type
decides how it resolves: declare an interface where a component should accept any of several
implementations, a concrete type where there's exactly one. Servo follows whichever you wrote and
never converts one into the other.

The one exception to "nothing else influences this" is step 0, and it is explicit by construction:
a marker in the spec file, written by you, is the only thing that can pre-empt the search.

Then the same three steps run for each of the selected provider's own parameters, recursively,
until the graph closes or a diagnostic is produced.

Two things sit *around* these steps rather than among them, both declared in the spec. A
[traced interface](tracing.md) resolves exactly as above, and then every request for it is
answered with the generated wrapper around that answer — so a `Bind`, a `Value` or an `Override`
decides what gets wrapped, never whether. And the wrapper's own dependency on
`trace.TracerProvider` is the one key whose exact-type step never looks outside your module, not
even at a lone candidate: the OpenTelemetry packages return that interface from several functions
of their own, none of which is the one you meant.

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

Emitted [HTTP servers](http.md) sit outside the level scheme entirely: they are machinery, not
nodes, wired after the last constructor and **before** the Init phase — every singleton they hand a
handler already exists by then, nothing has been initialised yet, and no node's level accounts for
them.

**Reachability.** The graph is the transitive closure of the declared roots — plus, when the spec
declares [`servo.HTTP()`](http.md), the emitted servers' own requirements, resolved as root-like
entry points through the same recursion: `*servo.HTTPConfig`, every served handler's dependencies,
every `servo.Use` middleware, and every `servo.Extract` extractor. Diagnostic chains label them
(`needed by handler app.Order (POST /order/…)`), exactly as a root labels its subtree. A perfectly
good candidate that nothing reaches is not an error and not a warning — it simply isn't in the
generated file. `servo why <type>` reports which root pulled a node in; `servo list` shows
everything that was a candidate, reachable or not.

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
- Everything whose order reaches the generated file — the `servo.Trace` wrappers, the config
  loaders `New` calls first — is ordered by the module a declaration lives in, then its file's
  slash-separated path within that module, then line and column: never by the directory a module
  happened to be read from, so moving a module a `servo.Include` names from `../a-shared` to
  `../z-shared` changes nothing, and Windows' `\` sorts no differently from `/`.
- Ambiguity candidate lists and diagnostics are ordered the same way, with the message breaking a
  tie between two at one position.
- The generated file embeds no source positions at all. Its header and `App.Graph()` name each
  provider by its function (`postgres.New`, `middleware.NewRecover`), so a comment added above a
  constructor, a different checkout path, a vendored copy of a dependency, or another patch release
  of Go — whose standard library sits at different line numbers — changes nothing, and
  `servo check` on another machine agrees. Positions are for reading, so the commands you read
  print them: `servo graph` writes a provider outside the module as its package's import path plus
  the file name (`github.com/okian/servo/v3/middleware/recover.go:18:6`,
  `$GOROOT/src/net/http/server.go:…`), never the module cache or replace directory it was read
  from; `explain`, `why` and `list` print absolute paths, for you and your editor.

One caveat: two different servo versions may legitimately produce two different, both correct,
files. Pin one for the whole project with `go get -tool github.com/okian/servo/v3/cmd/servo`, which
records the generator's version in `go.mod` — the `go:generate` directive then just says
`go tool servo generate`. `servo check`'s stale report names the version it is running for exactly
this case.
