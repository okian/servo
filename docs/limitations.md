# Limitations

**Who this is for:** anyone deciding whether to adopt servo. This page exists so you can find out
what it can't do now, rather than three weeks into a migration.

Everything here is a real boundary in the current release, checked against the source. None of it
is a roadmap item dressed up as a caveat.

## How to read this page

The limitations fall into three groups, and the difference between them genuinely matters:

**Consequences of resolving at build time.** These follow from the core idea — working out the
graph before the program runs. They won't change, because changing them would mean building a
different kind of tool. Treat them as permanent.

**Deliberate omissions.** These could be built. They haven't been, and there's a reason.

**Narrow shapes.** Rules about what servo will accept as a constructor. Most people meet these on
day one, and most have a straightforward answer.

If you're short on time: skip to [servo is the wrong tool if…](#servo-is-the-wrong-tool-if) at the
bottom. It's six lines and it'll tell you whether the rest is worth reading.

## Consequences of resolving at build time

### Everything is built once and lives for the whole process

The graph is constructed a single time, in the generated `New`. There's no per-request scope, no
transient lifetime, no factory that hands you a fresh instance on each call.

If a component needs a new object per request, it has to create that object itself. servo gives it
the thing that does the creating; it doesn't create one per call.

*What to do instead:* write a factory type by hand and inject that, like any other dependency.

### Nothing that only exists at runtime can be injected

Each constructor parameter has to be another node in the graph, resolved by its type. That rules
out anything that only comes into existence while the program runs: a `*testing.T`, a fixed clock
for a test, a `t.TempDir()` path, or the `ctx` handed to the generated `New`. `context.Context` gets
no special treatment, so a constructor asking for one has no provider by default.

This is the sharpest edge in the whole tool, and it's the reason mocking libraries need a small
hand-written adapter — their constructors want a `*testing.T`, and the graph has no way to supply
one. The README's Mocking section walks through the pattern for `moq`, `mockery`, and `gomock`.

*What to do instead:* for the general case, nothing. Take the value as a struct field and set it
after construction, or test that component directly instead of through the graph.

### The graph can't change based on the environment

One `servo.Build` call produces exactly one graph. There's no conditional wiring, no per-environment
variation, no feature-flagged nodes. You can't generate one graph for staging and a different one
for production.

*What to do instead:* put the variation behind an interface whose single implementation branches
internally, or build separate binaries that each have their own spec file.

### Your spec file is read, never run

`servo generate` parses your `servo.Build(...)` call as text. It never executes it. Each argument
has to be written out literally — `servo.Root[T]()`, `servo.Bind[I, C]()`, or
`servo.Override[I, C]()`, one per line.

You can't compute roots in a loop, hold them in a variable, spread a slice, or call a helper that
returns markers. If you find yourself wanting to, that's the tool telling you it's a static
description and not a program.

This is also why the file carries the `servoinject` build tag: it's excluded from your binary, and
`servo.Build` panics if it ever actually runs — which would mean the tag went missing.

### Normal Go tooling never checks your spec file

Because it sits behind an inactive build tag, `go build ./...`, `go vet ./...`, and `go test ./...`
all skip it. Your editor will grey it out unless you configure the `servoinject` build flag.

The practical consequence: a type error in your spec file shows up when you run `servo generate`,
not while you're writing it.

### A cycle is always a build failure

The graph has to be acyclic. There's no lazy or deferred construction that could break a cycle at
runtime. If two components genuinely need each other, one of them has to stop taking the other as a
constructor parameter — which is usually a design signal worth listening to, but it is a hard stop.

## Deliberate omissions

### There are no tags or names, so you can't have two of the same type

Identity in the graph is purely by type. Two constructors returning the same concrete type aren't
two instances — they're an ambiguity, and generation fails.

A primary and a replica database. Two SQS clients on two AWS accounts. Two tenant connections. None
of these can be expressed as two values of one type.

*What to do instead:* make them genuinely distinct types. Embed the shared client in two named
wrappers, each constructed differently:

```go
type Client struct{ Account string }         // the shared thing

type OrdersAccount struct{ *Client }         // two distinct graph nodes
type AuditAccount  struct{ *Client }
```

Because the wrappers embed `*Client`, every method still comes through — no delegation boilerplate.
This is the intended answer rather than a workaround, and the README has a complete worked version.

### You can't depend on "every implementation of X"

There's no way to ask for all providers of an interface as a slice. Plugin registries, middleware
chains assembled from packages that don't know about each other, and self-registering handler sets
have no direct expression.

This is `uber-go/fx`'s clearest advantage over servo — it calls the feature *value groups*. See
[How servo compares](comparison.md).

*What to do instead:* write one constructor that takes each implementation explicitly and returns
the slice. It works, but you maintain that list by hand.

### An override applies everywhere

`servo.Override[I, C]` is declared against an interface, not against a consumer. It replaces `I`
everywhere `I` is requested. You can't hand one service a fake repository while another one keeps
the real thing.

### You can't reach a component from outside its package

The generated `App` has only unexported fields, and no accessors are emitted. There's no `Get[T]()`
and no lookup by type — there's no container to ask. Tests that touch the graph have to live in the
injector's own package.

## Narrow shapes

These are the rules servo applies when deciding whether a function can be a constructor. You'll
likely meet one or two early on.

### A constructor must match one of four shapes

```go
func F(deps...) T
func F(deps...) (T, error)
func F(deps...) (T, func())          // cleanup function
func F(deps...) (T, func(), error)
```

Anything else is rejected with `does not match a supported result shape`. In particular, a
constructor can't produce two things at once — `func NewPair() (*A, *B)` isn't a valid provider.

### Variadic constructors are rejected

The options pattern — `func New(opts ...Option) *T` — cannot be a provider. A variadic parameter is
a slice underneath, and slices are never resolvable.

This one catches people, because the options pattern is common in Go. If a package you own uses it,
add a plain constructor alongside for servo to find.

### Generic functions are never providers

A function with type parameters can't be a constructor, whatever its shape.

### Only top-level functions count

Methods, function-typed package variables, and struct literals can never provide. It has to be a
top-level `func` declaration.

### The result type must be named, a pointer to a named type, or a non-empty interface

Primitives, slices, arrays, maps, and `any` are all rejected as results. You can't provide a bare
`string` for a connection URL, or an `int` for a port number.

*What to do instead:* wrap it in a named type — `config.Port` instead of `int`. This feels like
bureaucracy until you notice what it prevents: two unrelated `string` dependencies quietly
resolving to each other because they happen to share a type.

## servo is the wrong tool if…

- You need two instances of the same type and can't make them distinct types.
- You need to collect every implementation of an interface into a slice.
- Your graph is assembled dynamically, or varies by environment or feature flag.
- You need per-request or transient lifetimes.
- You need to pull components out of a container at runtime.
- Your service has five components and a `main.go` you're happy with. Write it by hand.

For the first three, `uber-go/fx` is the better tool, and
[How servo compares](comparison.md) says so directly. For the last one, nothing beats the code you
already have.

## If something here is wrong

If a limitation on this page is out of date, or you hit a boundary it doesn't name, open an issue
at [github.com/okian/servo/issues](https://github.com/okian/servo/issues).

A limitation that's written down is a trade-off you agreed to. One that isn't is a bug in this page.
