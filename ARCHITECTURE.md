# Architecture

This document explains how the pieces fit together and why they're shaped the way they are. For
usage and the CLI reference, see [README.md](README.md); for the one-line-per-package file list,
see its [Layout](README.md#layout) section; for a worked example of a real application consuming
servo end to end rather than servo's own internals, see [docs/tutorial/](docs/tutorial/README.md).

## Pipeline

Every command (`generate`, `check`, `graph`, `explain`, `why`, `list`, `doctor`) is a different
view onto the same stages, orchestrated by `cmd/servo/pipeline.go`:

```
load.Load             → *load.Loaded             one go/packages session: main module + everything
                                                   it transitively imports, fully type-checked
load.FindSpec(s)       → *load.Spec, ...          parse servo.Build(...)'s AST: roots, Bind/
                                                   Override/Scoped declarations, the injector
                                                   package
graph.ScanCandidates   → []*graph.Provider,       every constructor-shaped function in scope,
                          []graph.Rejected         classified; non-candidates kept with a reason
resolve.Resolve        → *resolve.Resolved        roots → transitive closure → selection
                                                   precedence → cycle detection → levels →
                                                   ordered plan, or diagnostics (never a partial
                                                   graph)
emit.Emit              → []byte                   the resolved plan → one deterministic,
  (or internal/render)                             gofmt-clean Go source file — or, for
                                                   `servo graph`, one of text/JSON/DOT/Mermaid
                                                   instead
```

`load.Load` and `graph.LoadCapabilities` run once per module directory regardless of how many
injectors it contains (`loadModule` in pipeline.go). Everything after that is per-injector: each
`servo.Build(...)` call in scope gets its own `*pipeline` (candidates, capabilities, main-module
scope), because a monorepo's `cmd/api`, `cmd/worker`, and `cmd/migrator` roots don't share a
resolved graph even though they share one type-checking session.

`generate` and `check` process every injector `--dir` finds in one pass (`buildPipelines`),
matching `wire ./...`'s discovery model. `explain`, `why`, `list`, and `graph` answer a question
about *one* graph, so they use `buildPipeline` (singular) and ask the caller to disambiguate with
`--dir` when a module has more than one.

Every path checks for build errors outside the injector(s) via `Loaded.NonInjectorErrors` before
resolving anything — `buildPipeline` excludes just the one injector path `FindSpec` already
resolved, while `buildPipelines` and `doctor` (which makes its own `FindSpecs` +
`NonInjectorErrors` call rather than going through `buildPipelines`) exclude every known injector
path at once, so checking injector B doesn't trip on injector A's legitimate pre-generation
`undefined: New`. `doctor` additionally aggregates every injector's health — spec found, generated
file present, fresh, tracked by git — into one report, which the single-graph commands don't do.

## Selection precedence

`internal/resolve` picks one provider per requested type in this order:
1. An explicit `servo.Bind[I, C]()`, or for `NewTestApp`, `servo.Override[I, C]()` — the spec
   file's own word overrides everything else.
2. An exact type match — a candidate whose return type is precisely the requested type.
3. Structural auto-bind — exactly one candidate in scope whose return type implements the
   requested interface (`types.Implements`).

Zero matches and more than one match both render through the same diagnostic shape ("servo: no
provider for ..." — `unresolvedDiagnostic` in `internal/resolve/diagnostics.go`); they differ only
in whether there's a candidate list worth suggesting. For an ambiguous interface, that's one
`servo.Bind[I, C]()` suggestion per implementer; for a concrete type produced by more than one
function, it's "remove or rename all but one" instead, since no `Bind` can disambiguate two
constructors that return the identical type. Every diagnostic also prints the full "needed by"
chain back to the root that demanded the missing type, not just the immediate consumer.

"Scope" for structural search is the whole main module (`mainModuleScope` in pipeline.go), not
just packages the requesting package itself imports — narrowing it that way would defeat the
purpose of depending on an interface in the first place, since the implementation living
somewhere the consumer doesn't import is exactly the common case. Third-party and stdlib
candidates are never in scope, which is what keeps deep dependency trees from producing false
ambiguity at scale.

## Scopes

A scope is the one part of the graph that isn't a singleton, and it threads through every stage
rather than bolting onto the end of one.

`internal/graph/scopekey.go` finds the `ScopeKey` extractor. It is the only thing servo detects
**by name** rather than with `types.Implements`: a `ScopeKey`'s dependency list varies per type, so
there is no single interface shape to match. The same file holds the blank-receiver check, which
`internal/resolve` and `cmd/servo-vet` both run — generated code calls the extractor on a typed
nil, and no signature can express "never dereferences the receiver".

`internal/resolve` resolves scopes **before** roots, deliberately. The check that gives the feature
its reason to exist — a singleton capturing a scoped instance — is a question about a node that the
scope pass has already classified, so it has to run first. Membership is then a fixpoint over the
resolved edges: a node belongs to a scope if it is a declared root of it, or if any of its
dependencies is that scope's key or is itself a member. Everything else the scope reaches is a
singleton it borrows, constructed once by the `App` and shared by every instance.

Two synthetic node kinds carry this through emission. `NodeScopeKey` stands for the extracted key
value, and `NodeScopeAccessor` for the generated accessor — neither is built by a provider, so
neither appears in any `Order`; both appear only in other nodes' `Deps`. Keeping them out of
`Resolved.Order` is what makes every pre-existing loop over that slice correct without change, and
is why an app with no `Scoped` declaration emits a byte-identical file.

Widening, cross-scope, extractor-cycle and undeclared-scope are all checked **after** traversal
rather than during it (`checkScopeEdges`), so each edge is examined exactly once no matter which
pass discovered it — including the ones a second scope's sub-graph reaches into a first scope's.
The needed-by chain those diagnostics print is reconstructed by BFS from the roots, which is why
they read identically to the ones resolution produces inline.

`internal/emit/scope.go` is the only place servo generates concurrent, stateful, timer-driven code.
Per scope: a registry. Per live key: an entry owning its reference count and linger timer as
loop-local variables in its own goroutine. That shape was chosen over a single per-scope `select`
because a slow teardown or a blocking construction in one key would otherwise freeze every acquire
in the scope, and because per-entry state reads better as a per-entry state machine — which also
removes any need for a generation counter, since a dying entry is never revived.

`Shutdown` sequences scopes into the existing reverse-dependency teardown by topological sort over
a combined graph of singletons and scopes (`shutdownSteps`), rather than appending them: a scope
has to stop after every singleton that could still call `Acquire`, and before every singleton its
instances depend on. With no scopes declared that sort re-derives `reverse(Order)` element for
element, which is what keeps the no-scope output byte-identical.

Golden files cannot catch a torn-down-while-live race, so the gate on this feature is
[`examples/scoped`](examples/scoped)'s suite: concurrent acquire of a cold key, eviction racing
acquire at the linger boundary, cancellation as the only release path, `Shutdown` racing in-flight
acquires, constructor failure under concurrency, and a thousand distinct keys — all under `-race`,
each ending in a goroutine-leak check.

## Canonical type identity

Two written-differently type expressions can name the same type: a defined alias
(`type DBAlias = *DB`), a pointer to one, or an identical type reachable through two different
import paths. `internal/graph.Key` is what the rest of the pipeline uses to decide "is this the
same node" — built from `types.Unalias`, recursed through `*types.Pointer` (`unaliasDeep`), so
`*DB` and `*DBAlias` collapse to one graph node instead of two that happen to construct identical
values.

## Why the markers panic

`servo.Build`, `Root`, `Bind`, and `Override` (in the `servo` package) all panic unconditionally
if actually called at runtime. They exist to be read as AST syntax by `load.FindSpec`, inside a
file carrying the `servoinject` build tag — a tag that's never set for the real binary, so the
file, and the marker calls in it, compile out entirely. The panic is the fallback for the one way
this can go wrong: the tag is missing, the marker call compiles into the real binary, and it runs.
`cmd/servo-vet` is the static safety net for that same failure mode: a go/analysis pass flagging
any marker call in a file that doesn't carry the tag, catchable in the editor before `go generate`
or a build ever runs. It carries one other rule for the same reason — a `ScopeKey` method whose
receiver its body can reach, which the type system cannot rule out and which generated code turns
into a nil dereference.

## Test overrides

A `servo.Override[I, C]()` in the spec produces a second resolve pass — the same candidates and
capabilities, with `C` merged in ahead of whatever `Bind` or structural search would otherwise
choose for `I` (`pipeline.resolve`'s `extraBinds` parameter) — emitted as `NewTestApp` in a
separate `servo_gen_test.go` (`generateOne` in `cmd/servo/generate.go`), so the override only
exists while testing and `New` in the real `servo_gen.go` is unaffected.

## Writes are atomic, checks never write

`servo generate` writes by creating a temp file in the target's own directory and renaming it into
place (`writeFileAtomic`), never by truncating and rewriting the target directly — a process
killed mid-write leaves the previous, complete file untouched instead of a truncated one. `check`
never writes at all: it re-runs the same resolve-and-emit and diffs the result against what's
already committed (`checkOne` in `cmd/servo/check.go`), so CI can catch drift without a mutating
step.

## Everything else

Package-level doc comments (`go doc ./...`) are the authoritative reference for any single
package's API. Godoc `Example` functions in `servo` and `servotest` show real, executed usage
where the API allows it — some exported functions there panic by design or require a `*testing.T`
that an `ExampleXxx` function has no way to supply, so not every function has one; see the
comments in `servo/example_test.go` and `servotest/example_test.go` for which and why.
`internal/emit`'s golden-file tests (`internal/emit/testdata/golden/fullapp.go.golden` and
`scopedapp.go.golden`, both refreshed with `UPDATE_GOLDEN=1`) are the authoritative reference for
exactly what emitted code looks like.
