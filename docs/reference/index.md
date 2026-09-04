# Reference

**Who this is for:** anyone who already knows roughly what servo does and now needs a specific
answer — what a flag does, what a method guarantees, why a function wasn't picked up as a
provider, what a particular error message means.

Everything on these pages is written against the source of the current release. Where behaviour is
narrow, surprising, or easy to get wrong, it says so rather than describing an idealised version of
the tool. If you want to *learn* servo instead of look something up, the
[tutorial](../tutorial/) builds a real service with it, and the [preface](../preface.md) explains
dependency injection from scratch.

## The three surfaces

Servo is small, and almost everything about it falls into one of three surfaces. Knowing which one
your question is about is usually enough to pick a page.

**The command you run.** `servo` is a code generator you invoke from a terminal or a
`go:generate` directive. It reads your module, resolves a graph, and writes a file.
→ [CLI commands](cli.md)

**The file you write.** One spec file per injector, carrying a `servo.Build(...)` call. It is read
as syntax and never executed, which is why it lives behind a build tag.
→ [Spec file and markers](spec.md), then [Resolution rules](resolution.md) for how the graph is
worked out and [Diagnostics](diagnostics.md) for what happens when it can't be.

**The code you get.** A generated `App` type with a fixed set of methods, calling into a small
runtime package. This is what actually runs in production.
→ [Lifecycle](lifecycle.md) for the contract, [Generated API](generated-api.md) for the exact
shape, [servo package](servo-package.md) for the types those methods return.

One thing sits across all three surfaces rather than inside any of them: a **scope**, the only part
of the graph that is not built once and held for the life of the process. It has its own method to
write, its own marker, its own generated code, and its own diagnostics.
→ [Scoped instances](scopes.md)

## Find it by question

| You want to know | Page |
| --- | --- |
| What every command and flag does | [CLI commands](cli.md) |
| Why my spec file needs a build tag | [Spec file and markers](spec.md) |
| The difference between `Bind` and `Override` | [Spec file and markers](spec.md#bind) |
| Which function shapes count as constructors | [Resolution rules](resolution.md#what-counts-as-a-provider) |
| Why servo can't see the constructor I wrote | [Resolution rules](resolution.md#rejection-reasons) |
| How an interface parameter gets matched to an implementation | [Resolution rules](resolution.md#selection-precedence) |
| What an error message means and how to fix it | [Diagnostics](diagnostics.md) |
| How to get one instance per tenant, room, or region | [Scoped instances](scopes.md) |
| How to load settings from env vars and a config file | [Generated configuration](config.md) |
| What `//servo:config` and its tags mean | [Generated configuration](config.md#the-tag-grammar) |
| Every setting my binary reads, as a table | [CLI commands](cli.md#config) |
| Why a singleton can't depend on a scoped type | [Scoped instances](scopes.md#diagnostics) |
| When a scoped instance is actually torn down | [Scoped instances](scopes.md#lifetimes) |
| The seven lifecycle methods and when each is called | [Lifecycle](lifecycle.md#the-seven-capabilities) |
| What happens when a component refuses to stop | [Lifecycle](lifecycle.md#the-stop-budget) |
| The signature of every method on the generated `App` | [Generated API](generated-api.md) |
| How generated field names are chosen | [Generated API](generated-api.md#field-names) |
| Every exported identifier in `servo` | [servo package](servo-package.md) |
| Every exported identifier in `servotest` | [servotest package](servotest-package.md) |

## What is not here

**Runnable examples.** They live in the repository, as real modules that build and test in CI:
[`examples/basic`](https://github.com/okian/servo/tree/master/examples/basic) for the whole feature
surface, [`examples/mocking`](https://github.com/okian/servo/tree/master/examples/mocking) for the
three mock-library integrations,
[`examples/scoped`](https://github.com/okian/servo/tree/master/examples/scoped) for keyed,
refcounted instances and the race suite that gates them,
[`examples/diagnostics`](https://github.com/okian/servo/tree/master/examples/diagnostics) for
permanently broken fixtures that each print one diagnostic,
[`examples/variants`](https://github.com/okian/servo/tree/master/examples/variants) for one
injector resolved into two graphs behind a build tag, and
[`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial) for the
service the tutorial builds.

**Design rationale.** Why the pipeline is shaped the way it is —
[ARCHITECTURE.md](https://github.com/okian/servo/blob/master/ARCHITECTURE.md).

**What servo cannot do.** Deliberately separated out, so it can be read before adopting rather
than discovered during — [Limitations](../limitations.md).

**Generated Go doc comments.** The `servo` and `servotest` packages are also on
[pkg.go.dev](https://pkg.go.dev/github.com/okian/servo/v3). The pages here cover the same
identifiers with the surrounding behaviour that a doc comment has nowhere to put.

## A note on stability

The generated file is an implementation detail of your own package: you commit it, but you don't
edit it, and its exact contents can change between servo releases. What is stable is the *API* of
what it generates — the method set on `App` described in
[Generated API](generated-api.md) — and the JSON schema of `servo graph --format=json`, which is
the same schema `App.Graph()` serialises to.
