# How servo compares

**Who this is for:** anyone deciding which wiring approach to use, including people who haven't
used any of these tools. If you're not sure what dependency injection is for in the first place,
read the [Preface](preface.md) first — it's a shorter path in.

This page won't tell you servo is the best choice, because for a lot of projects it isn't. What it
will do is tell you which of these five approaches fits your situation, and why.

## First, three words you'll need

Comparisons of these tools tend to assume you already speak the dialect. Three terms do most of the
work, so here they are up front.

**Container.** An object you hand your constructors to at runtime. It looks at what each one needs,
works out the order, and builds everything while your program starts. `fx` and `dig` are
containers. servo and wire are not — they do their work before the program runs, so there's nothing
to hand anything to.

**Provider set.** A named, reusable bundle of constructors. Instead of listing forty functions in
one place, you group them — `databaseSet`, `httpSet` — and compose the groups. Useful when several
binaries share most of their wiring. `wire` has these. servo has something narrower —
`servo.Include`, a shared list of *markers* rather than of constructors — and the difference is
spelled out [below](#googlewire).

**Value group.** A way of saying "collect *every* constructor that produces a `Handler`, and give
me all of them as a slice." It's how you build plugin registries and middleware chains where the
pieces don't know about each other. `fx` and `dig` have these; servo and wire don't. (servo does
compose HTTP middleware chains, but by a different mechanism: each `servo.Use` is named explicitly
in the spec, an ordered list rather than a collection the pieces join themselves.)

That last one matters more than it sounds, and it comes up again below.

## The two questions that actually separate these tools

Feature checklists aren't much help here, because these tools mostly agree on features. Two
questions do the real work:

1. **When do you find out the wiring is wrong?** When you build, when the process starts, or never.
2. **Who handles startup and shutdown?** Whoever works out the dependency order already knows what
   order to start things in. Some tools use that knowledge. Some hand it back to you.

Everything below follows from those two.

## At a glance

| | How it works | Errors surface | Startup/shutdown | Generated HTTP transport | Reflection at runtime | Code you can read | Two of the same type | "All implementations of X" | Keyed instances |
|---|---|---|---|---|---|---|---|---|---|
| **servo** | Reads constructors, generates Go | Build time | Built in | Yes — opt-in, emitted | No | Yes — `servo_gen.go` | Via distinct types | No | Yes — scopes, with lifecycle |
| **google/wire** | Reads constructors, generates Go | Build time | You write it | No | No | Yes — `wire_gen.go` | Via distinct types | No | No |
| **uber-go/fx** | Container | `fx.New` at startup, or a test calling `fx.ValidateApp` | Built in | No | Yes | No | Yes — name tags | Yes — value groups | Roll your own |
| **uber-go/dig** | Container | First `Invoke` | You write it | No | Yes | No | Yes — names | Yes — groups | Roll your own |
| **By hand** | You write it | Compile time | You write it | You write it | No | It *is* your code | Yes — variables | Yes — a slice | Roll your own |

Two of servo's cells are the honest price of identifying everything by type before the program
runs. "All implementations of X" is a flat no. "Two of the same type" works, but only the way it
works in wire: the two instances become two named types, rather than two tagged values of one type.
Don't skim past either — if you need value groups, or two instances of a type you can't give two
names, that decision is already made, and it isn't servo.

The last column runs the other way. Every container here can hand you a value keyed at runtime, if
you write the registry yourself; none of them checks that a singleton didn't capture one of those
values and pin it for the life of the process. servo's [scopes](reference/scopes.md) generate the
registry — reference counting, linger window, per-instance lifecycle — and make that capture a
build error. It is the one place where resolving at build time buys something the runtime
containers structurally cannot offer.

## google/wire

The closest relative to servo, and the natural comparison. Wire also reads your constructors at
build time, also resolves the graph then, and also writes plain Go to disk — `wire_gen.go` instead
of `servo_gen.go`. If servo's model appeals to you, wire's will too. They're answering the same
question.

Two differences matter.

**Wire doesn't do lifecycle.** It builds your object graph and hands you the root object. Starting
things up, shutting them down in reverse, health checks, shutdown timeouts — all of that stays your
job, in the `main.go` you were hoping to shrink. Wire does support cleanup functions, so a
constructor can return a `func()` that runs on teardown, but that's resource cleanup, not an
application lifecycle. There's no notion of "start the server after the database is ready."

This is the single biggest structural difference between the two tools, and it's most of the reason
servo exists — and the same line now covers the transport half, since servo can also emit the HTTP
servers themselves from `//servo:` directives, which wire has no analogue for.

**Wire has provider sets. servo has something narrower.** With wire you build up named bundles of
constructors and compose them:

```go
// wire: compose sets explicitly, then declare the injector
var appSet = wire.NewSet(postgres.New, orders.New, api.New)

func initApp() (*api.Server, error) {
	wire.Build(appSet)
	return nil, nil
}
```

With servo you name what you want, and reachability handles the rest:

```go
// servo: name the root; everything it needs is implied
servo.Build(
	servo.Root[*api.Server](),
)
```

Neither is obviously better. servo's version is shorter and there's less to maintain; wire's is a
real unit of composition.

For the reuse half of that, servo has `servo.Include(fn)` — a function returning `[]servo.Marker`,
spliced into a `Build` call as syntax and never called, shared by as many injectors as you like,
with a locally-written marker overriding an included one. Four binaries in
[`examples/tutorial`](https://github.com/okian/servo/tree/master/examples/tutorial) use it to share
the markers below their transports that used to be copy-pasted into each 53-line spec file; a spec
is now the `Include` plus the one thing that differs.

It is not a provider set, and the difference is worth being exact about rather than blurring. A
wire set bundles *constructors*: it decides what is available to build from, so composing two sets
composes two lists of functions and a set is how you scope wiring. An included servo set is a list
of *markers* — `Bind`, `Override`, `Scoped`, `Value` — so it changes how the graph resolves, not
what is in scope. servo's candidate set is every constructor-shaped function in the main module, all
the time, and what gets built is whatever the roots reach. `Include` therefore gives you the shared
wiring; it does not give you wire's "these constructors and no others."

**Wire's injectors take parameters; servo declares them instead.** A wire injector can accept
arguments that become graph inputs, which is how a value the program only has at runtime — a parsed
config, an open `*sql.DB` — gets in. servo's equivalent is `servo.Value[T]()` in the spec, which
makes the generator emit a `Values` struct and a `NewWith(ctx, Values{...})` beside `New`. Same
idea, declared in the spec file rather than in a signature you write; the remaining boundary is
that it is supplied once per app, so a per-call value still has no way in. One input needs no
declaration on either side: wire hands an injector's `context.Context` argument to any provider
that asks for one, and a servo constructor taking a `context.Context` receives the `ctx` that `New`
was called with.

One more thing worth knowing: wire is no longer maintained. Google archived the repository in
August 2025, and its README now asks anyone who wants to update or extend it to do so in a fork.
Code it already generated is plain Go and keeps compiling. The generator is the exposed part: it
reads your packages through `golang.org/x/tools`, which has to keep pace with each Go release, and
wire's last commit before the archive was exactly that kind of update, to build with Go 1.25.

**Pick wire when** you want build-time code generation and nothing more, you already handle
lifecycle in a way you're happy with, or you need provider sets as a unit of composition — deciding
which constructors are in scope, not only which bindings apply to them — and you are comfortable
running an archived tool, or a fork of it.

## uber-go/fx

fx is the most capable tool on this page, and it's stronger than servo in ways worth stating
plainly rather than burying.

It's a container: you call `fx.Provide` with your constructors and `fx.Invoke` with the things that
should actually run, and fx works out the graph using reflection as your app starts. Its lifecycle
model — `OnStart` and `OnStop` hooks — is mature and has been running Uber's production services
for years. This is not a toy.

**fx can express one thing servo simply cannot, and another that servo spells differently.**

Value groups let a component depend on every implementation of an interface at once. If you're
building a plugin registry, a middleware chain assembled from independent packages, or a set of
handlers that register themselves, fx does this natively and servo has no equivalent.

Named values let you have two instances of the same type — a primary database and a read replica,
both `*sql.DB` — distinguished by a string tag. servo has no tags; it gets the same two instances
by making them two types (`type Primary struct{ *sql.DB }`, `type Replica struct{ *sql.DB }`),
which is also wire's answer. That costs a declaration per instance, and buys a name the compiler
checks where fx matches a string while the app starts.

If your graph needs value groups, or two instances of a type you can't wrap in two types of your
own, servo is the wrong tool, and no amount of reading further will change that.

**What servo gives up in exchange is the reflection.** An fx wiring mistake isn't a compile error.
fx builds the graph when `fx.New` runs, so a missing dependency or a cycle is reported as the
process starts — a clear message naming the constructor, the missing type and its file and line,
which `app.Err()` returns and which stops `Start` and `Run` from going further. fx also lets you
find it sooner:
`fx.ValidateApp` checks a set of options the same way without calling any constructor, so a test
that hands it the options `main` uses fails CI instead of a deploy. That test is yours to write and
to keep in step with `main`, and it checks the options it is given, not the ones an environment
assembles at run time. servo has no equivalent test to forget: the check *is* generation, and
[`servo check`](servo-in-ci.md) fails CI when a committed file no longer matches the code. There's
also a startup cost to reflection, though for most services that's a rounding error and not a real
argument.

**Pick fx when** you need value groups or named instances, you want a lifecycle with a long
production track record behind it, or your graph is genuinely dynamic — assembled from modules that
vary at runtime.

The one place the comparison runs the other way is per-key instances. fx has no scope concept: a
per-tenant or per-room instance is a registry you write and maintain, with its own reference
counting and its own teardown, and nothing checks that a singleton didn't capture one.

## uber-go/dig

dig is the container underneath fx, available on its own. You get `Provide` and `Invoke`, named
values, groups, and optional dependencies — and then it stops. No lifecycle, no module system, no
app runner.

That makes it a good pick if you're building your own framework and want the container as a
primitive, and a poor one if you wanted the framework. Compared to servo the trade is the same as
fx's, minus the lifecycle: your errors show up at the first `Invoke` instead of at build time, and
there's no generated artifact to read or review.

**Pick dig when** you're building your own application framework and want the container without
opinions attached.

## Writing it by hand

This is what most Go teams do, and Go's culture leans this way for good reasons. It deserves more
than a dismissive paragraph.

Hand-written wiring has no tool to install, no generated file to commit, no build tag, no
regeneration step, and no possibility of a code generator being wrong. Everything is visible in one
function. For a service with five components, this is simply better, and any comparison page that
can't bring itself to say so is selling you something.

The question isn't really component count. It's whether you've hit these two specific pains:

- **Ordering that's become load-bearing** — startup must happen in one order, shutdown in exactly
  the reverse, and the two are separate code that quietly drifts apart.
- **The four-place edit** — adding one component means changing construction, wiring, startup, and
  shutdown, and the compiler only catches some of it.

Before you feel both of those, hand-written wiring is the right call. After you feel both, you're
maintaining a hand-written version of what servo generates, and doing it without a compiler
checking your work.

**Write it by hand when** the graph is small, the ordering is obvious, or your project can't take
on a code generator and a committed generated file.

## The honest summary

servo's argument is narrow and specific: it's the only tool here that resolves the graph at build
time *and* manages the lifecycle *and* leaves you an artifact you can read — and, opted into, that
artifact now includes the HTTP servers themselves. Miss a dependency and the build stops. Give a
component a `Stop` method and it gets called, in the right order, under a budget, with a report at
the end.

Against that: servo gives you two instances of one type only as two distinct types, can't collect
every implementation of an interface, can't vary the graph at run time, and can't hand you a
component at runtime. fx does all four. Wire is a reasonable choice if you don't want lifecycle
management and can live with an archived generator. Hand-written wiring is a reasonable choice for
a small service and always will be.

If you're leaning toward servo, read [Limitations](limitations.md) next — it's the fastest way to
find out whether one of those "can't"s is a dealbreaker for you. If you're moving from wire or fx,
[Coming from wire or fx](coming-from-wire-and-fx.md) maps each of their concepts onto servo's.
