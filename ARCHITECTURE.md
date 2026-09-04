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
                                                   Override/Scoped/Value declarations, any
                                                   Include spliced in place, the injector
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

## Included marker sets

`servo.Include(fn)` is resolved in `internal/load/spec.go`, not by a later stage: `spliceInclude`
locates the named function's declaration and hands its returned slice literal back to
`parseMarkerArgs` — the same function that reads `Build`'s own argument list, recursively, rather
than a second reader that could drift from it. Everything downstream therefore sees one flat
`*load.Spec` and cannot tell an included declaration from a locally-written one.

Three things fall out of it being syntax rather than a call. The declaration is found by walking
the injector package's import graph (`allPackages`, breadth-first and deduplicated by path),
because a shared marker set living in its own package is the whole reason the marker exists; its
file must require the `servoinject` tag, checked with the same `FileRequiresBuildTag` a spec file
is checked with, since every marker in it panics if it ever runs; and its body must be exactly
`return []servo.Marker{...}`, because a variable, a conditional or an `append` would be a program
servo has to *execute* to know the answer to. Nesting is allowed and the `including` chain is
carried down each recursion, so a cycle is reported with the path that closed it instead of
recursing forever.

Ordering is the one place the flattening is visible: markers are appended where the `Include` sits,
so a `Bind`/`Override` written later in the spec file finds a prior declaration for the same
interface and replaces it when that prior one came from an include (`BindDecl.Included`). Two local
declarations for one interface remain the duplicate error they always were.

## Selection precedence

`internal/resolve` picks one provider per requested type in this order:
1. A `servo.Value[T]()` — the spec file said this one comes from the caller, so no provider is
   consulted at all, not even an explicit `Bind` for the same type. (`resolveKey` checks
   `suppliedByKey` before it reaches `selectProvider`, for the same reason a declared scope
   accessor is checked before structural search.)
2. An explicit `servo.Bind[I, C]()`, or for `NewTestApp`, `servo.Override[I, C]()` — the spec
   file's own word overrides everything else.
3. An exact type match — a candidate whose return type is precisely the requested type.
4. Structural auto-bind — exactly one candidate in scope whose return type implements the
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

## Supplied values

A `servo.Value[T]()` enters as a fourth `NodeKind` — `NodeSupplied` — and rides the same trick the
two scope kinds do: it never appears in `Resolved.Order`, only in other nodes' `Deps`. Nothing
constructs it, so there is nothing for the construction loop to emit, and every pre-existing loop
over `Order` stays correct with no `Kind` check added to it. `Resolved.Supplied` carries them
separately, in declaration order, for the emitter to turn into fields.

It is also the first node kind with no `Provider` that anything still has to *render*. The two
scope kinds appear only as entries in some other node's `Deps`, where a key string is all a renderer
needs; a supplied value is a node in its own right, and every stage that used to reach straight
through `n.Provider` for a result type and a position now reads `Kind` first and falls back to
`SuppliedType`/`SuppliedPos` on the node itself. `internal/emit/graphdata.go`, `internal/render`,
`servo explain` (binding "supplied", provider "the caller, via NewWith") and `servo why` all take
that branch, which is why a supplied value appears in `App.Graph()` and in every view of the graph
rather than existing only inside the constructor.

`internal/emit/values.go` is the emission side: the `Values` struct (`TestValues` in the override
variant), `NewWith` carrying the real constructor body, and a `New` that delegates to it with a
zero `Values{}` — so the generated method set is the documented one whether or not a value is
declared. All of it is gated on `len(e.values) > 0`, which is what makes a graph declaring no value
emit a byte-identical file, the same property the no-scope path has and for the same reason.

A declared value nothing in the graph depends on never reaches emission: `resolveKey` marks
`suppliedUsed` as it hands one out, and `checkSuppliedValues` turns the remainder into diagnostics.
Unused, it would be a field every caller fills in and the app never reads.

## Generated configuration

A `//servo:config` type enters the pipeline in `loadModule` (`graph.ScanConfigs`, main module
only, strict — a malformed directive is a module-wide error like a build error, because the
author wrote it and servo never silently half-honors a directive) and resolves as a fifth
`NodeKind`, `NodeConfig`, riding the `NodeSupplied` trick exactly: never in `Resolved.Order`,
carried in `Resolved.Configs`, so every pre-existing loop over `Order` stays correct and a graph
using no config emits a byte-identical file. It short-circuits ahead of `selectProvider` the way
a declared accessor does — which is also why a hand-written constructor for a config type is a
diagnostic rather than a silent loser — while a `servo.Value` for the same type still wins, per
precedence rule 1.

Emission is split in two. The loader (`internal/emit/config.go`) is a *companion file*,
`servo_config_gen.go`, written into the config type's own package — the placement is the feature:
it is what lets the type and its fields stay unexported, which no reflection-based env library
can do. It is gated `!servoinject` like every generated file, so generation never sees the
previous run's output, and its one export (`ServoConfig`) exists because the injector is another
package and Go has no narrower door. The injector side (`internal/emit/configfile.go`) loads each
config at the top of `New` as a **local, never an `App` field** — a field of an unexported
foreign type would not compile — which is also why a scoped constructor depending on a config is
a resolve-time diagnostic: scoped constructions read borrowed singletons off the `App`.

`servo.ConfigFile` makes the loader's signature take the decoded file map, and the signature is
the reason for the one cross-injector rule in the feature: a shared companion is one file with
one signature, so every injector using a config must declare a file or none.
`runGenerate`/`runCheck` resolve every injector before writing anything and refuse the mix
(`checkConfigAgreement`), for the same reason the variant-overlap check runs before any write.
The declared extension picks the decoder the generated code imports — in the *user's* module, so
servo's own `go.mod` carries no yaml or toml dependency, and an env-only or JSON app stays
stdlib-pure. The `conf` package exists because the three decoders disagree about numbers (JSON
`float64`, yaml.v3 `int`, TOML `int64`); that normalization is written once, stdlib-only, with
errors that name types and never values — a config file holds secrets.

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

`servo.Build`, `Root`, `Bind`, `Override`, `Scoped`, `Value`, and `Include` (in the `servo`
package) all panic unconditionally if actually called at runtime. They exist to be read as AST
syntax by `load.FindSpec`, inside a file carrying the `servoinject` build tag — a tag that's never
set for the real binary, so the file, and the marker calls in it, compile out entirely. The panic
is the fallback for the one way this can go wrong: the tag is missing, the marker call compiles
into the real binary, and it runs.

`servovet` is the static safety net for that same failure mode: a go/analysis pass flagging any
marker call in a file that doesn't carry the tag, catchable in the editor before `go generate` or a
build ever runs. `Include` is the marker most likely to trip it, because an included set lives in
its own package rather than beside a `main.go`, which is where the tag is easiest to forget — the
loader refuses that case too, but a refusal at generation time is later than an editor squiggle. It
carries one other rule for the same reason — a `ScopeKey` method whose receiver its body can reach,
which the type system cannot rule out and which generated code turns into a nil dereference.

The analyzer is its own importable package rather than a `var` in `package main`: golangci-lint's
module plugin system, a multichecker, and `analysistest` all take an `*analysis.Analyzer` value,
and nothing can take one out of a command. `cmd/servo-vet` is now the `singlechecker` wrapper
around `servovet.Analyzer` plus one thing that has to live in the binary — the refusal of
`-tags=`, which go/analysis registers on every singlechecker and documents as a no-op, so a run
that looks like it covered the `prod` configuration silently covered the default one.

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
