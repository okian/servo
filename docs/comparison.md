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
binaries share most of their wiring. `wire` has these; servo doesn't.

**Value group.** A way of saying "collect *every* constructor that produces a `Handler`, and give
me all of them as a slice." It's how you build plugin registries and middleware chains where the
pieces don't know about each other. `fx` and `dig` have these; servo and wire don't.

That last one matters more than it sounds, and it comes up again below.

## The two questions that actually separate these tools

Feature checklists aren't much help here, because these tools mostly agree on features. Two
questions do the real work:

1. **When do you find out the wiring is wrong?** When you build, when the process starts, or never.
2. **Who handles startup and shutdown?** Whoever works out the dependency order already knows what
   order to start things in. Some tools use that knowledge. Some hand it back to you.

Everything below follows from those two.

## At a glance

| | How it works | Errors surface | Startup/shutdown | Reflection at runtime | Code you can read | Two of the same type | "All implementations of X" |
|---|---|---|---|---|---|---|---|
| **servo** | Reads constructors, generates Go | Build time | Built in | No | Yes — `servo_gen.go` | No | No |
| **google/wire** | Reads constructors, generates Go | Build time | You write it | No | Yes — `wire_gen.go` | Via distinct types | No |
| **uber-go/fx** | Container | App startup | Built in | Yes | No | Yes — name tags | Yes — value groups |
| **uber-go/dig** | Container | First `Invoke` | You write it | Yes | No | Yes — names | Yes — groups |
| **By hand** | You write it | Compile time | You write it | No | It *is* your code | Yes — variables | Yes — a slice |

Two of servo's cells say "No." Those are real, and they're the honest price of resolving everything
before the program runs. Don't skim past them — if either one is something you need, that decision
is already made, and it isn't servo.

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
servo exists.

**Wire has provider sets; servo doesn't.** With wire you build up named bundles of constructors and
compose them:

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

Neither is obviously better. servo's version is shorter and there's less to maintain. Wire's gives
you a real unit of reuse when several binaries share wiring, which servo has no answer for.

One more thing worth knowing: wire is in maintenance mode. It works, it's widely deployed, and it
isn't gaining features.

**Pick wire when** you want build-time code generation and nothing more, you already handle
lifecycle in a way you're happy with, or you need provider sets to share wiring across several
binaries.

## uber-go/fx

fx is the most capable tool on this page, and it's stronger than servo in ways worth stating
plainly rather than burying.

It's a container: you call `fx.Provide` with your constructors and `fx.Invoke` with the things that
should actually run, and fx works out the graph using reflection as your app starts. Its lifecycle
model — `OnStart` and `OnStop` hooks — is mature and has been running Uber's production services
for years. This is not a toy.

**fx can express two things servo simply cannot.**

Value groups let a component depend on every implementation of an interface at once. If you're
building a plugin registry, a middleware chain assembled from independent packages, or a set of
handlers that register themselves, fx does this natively and servo has no equivalent.

Named values let you have two instances of the same type — a primary database and a read replica,
both `*sql.DB` — distinguished by a tag. servo can't do this either.

If your graph needs either one, servo is the wrong tool, and no amount of reading further will
change that.

**What servo gives up in exchange is the reflection.** An fx wiring mistake isn't a compile error.
It's a failure at `app.Start()`, reported through a reflection-built error chain, in whichever
environment happened to run the binary first. The equivalent servo mistake stops your build with a
filename and a line number. There's also a startup cost to reflection, though for most services
that's a rounding error and not a real argument.

**Pick fx when** you need value groups or named instances, you want a lifecycle with a long
production track record behind it, or your graph is genuinely dynamic — assembled from modules that
vary at runtime.

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
time *and* manages the lifecycle *and* leaves you an artifact you can read. Miss a dependency and
the build stops. Give a component a `Stop` method and it gets called, in the right order, under a
budget, with a report at the end.

Against that: servo can't give you two instances of a type, can't collect every implementation of
an interface, can't vary the graph by environment, and can't hand you a component at runtime. fx
does all four. Wire is a reasonable choice if you don't want lifecycle management. Hand-written
wiring is a reasonable choice for a small service and always will be.

If you're leaning toward servo, read [Limitations](limitations.md) next — it's the fastest way to
find out whether one of those "can't"s is a dealbreaker for you.
