# Preface

**Who this is for:** anyone who writes Go and has never used a dependency injection tool — or has
heard the term and decided it sounded like something from another language. You don't need to know
what "DI" means. You don't need to have used `wire` or `fx`. By the end of this page you'll know
what problem servo solves, whether you actually have that problem, and where to go next.

It takes about eight minutes to read.

## The file that grows

Almost every Go service starts with a `main.go` you could read out loud over the phone.

```go
func main() {
	db := postgres.Open(os.Getenv("DATABASE_URL"))
	srv := api.New(db)
	http.ListenAndServe(":8080", srv)
}
```

Four lines. Nothing hidden, nothing clever, no indirection. If someone new joins the team, this is
the file you show them first.

Then the service does what services do — it grows.

A cache goes in front of the database, so now the cache has to be built after the config but
before the service that uses it. A message publisher arrives with a connection of its own. Someone
decides the server shouldn't accept traffic until Postgres has answered a `Ping`, so now there's
an ordering. That ordering isn't written down anywhere. It's just *implied* by which line comes
first.

Later, someone adds graceful shutdown. And here's where it gets genuinely unpleasant: shutdown has
to happen in exactly the reverse order. So now there are two functions that have to stay perfect
mirrors of each other, and nothing in the language will tell you when they drift apart.

## What's actually going wrong

It's worth being precise about this, because "main.go got big" isn't really the problem. Big files
are fine. Two specific things are going wrong.

**The order is load-bearing but invisible.** The sequence of statements in `main` encodes real
knowledge — that the cache needs the config, that the server needs the cache. But that knowledge
only exists as line order. Move two lines and you may have broken something the compiler is
perfectly happy about.

**Adding one component means four edits.** Construct it. Pass it to whatever needs it. Start it.
Stop it. The compiler catches the first two, because those are type errors. It has nothing to say
about the last two. So the failure mode isn't a red build — it's the NATS connection you forgot to
close, surfacing as a slow goroutine leak six weeks later.

Those two problems are what everything below is about.

## This has a name, and the name is worse than the thing

Handing a component the things it needs, rather than letting it go and find them itself, is called
**dependency injection**.

If that phrase makes you think of Spring, XML config files, and annotations that do things you
can't see — that's fair, and it's not what happens in Go. In Go, dependency injection looks like
this:

```go
func New(db *postgres.DB, cache *redis.Cache) *OrderService
```

That's it. That's the whole idea. A constructor takes what it needs as parameters instead of
reaching for a global or opening its own database connection. There's no framework involved, no
annotations, no runtime magic.

Which means if you've written a constructor like that, you've been doing dependency injection this
whole time, and nobody needed to tell you. The term is doing far more work to sound complicated
than the idea deserves.

So the interesting question was never *whether* to inject dependencies. You already do. The
question is **who writes the wiring** — the code that knows `postgres.New` has to be called before
`orders.New`, and carries the result from one to the other.

There are three answers.

## Answer one: you write it

This is what most Go teams do, and for a lot of services it's the correct choice. It's explicit,
there's nothing to install, nothing to learn, and no tool that can be wrong. You can read the
whole graph by reading one function.

It stops being the right answer at a specific point: when the ordering becomes load-bearing, and
when the shutdown path becomes a hand-maintained mirror image of the startup path. That's the
`main.go` from the top of this page, four months in.

## Answer two: a container works it out while the program starts

You hand a library your constructors. It uses reflection to look at their parameter types, figures
out what depends on what, and builds everything in the right order as the process boots.

A **container** here just means an object you register constructors with, which assembles them for
you at runtime. This is the model used by `uber-go/fx` and `uber-go/dig`, and they're good at it.

The trade is that a wiring mistake stops being a compile error. Forget to register something and
the code still builds, still passes review, still ships. It fails when the process starts — in a
stack trace that runs through reflection, in whichever environment ran it first. Often that's
staging. Sometimes it isn't.

## Answer three: a tool works it out before you build

Your constructors get read at build time. The graph is resolved then, on your machine, before
anything runs. The result is written out as ordinary Go source that you can open and read.

Forget to provide something, and you don't get a runtime surprise. You get a build failure, with a
filename and a line number.

This is what `google/wire` does, and it's what servo does.

## What servo actually does

Start with constructors. Plain ones. None of them import servo — that's not a simplification for
the sake of the preface, it's the actual design:

```go
func New(cfg *config.Config) (*postgres.DB, error)   // package postgres
func New(db *postgres.DB) *OrderService              // package orders
func New(s *OrderService) *Server                    // package api
```

Then write one small file naming what you want built:

```go
//go:build servoinject

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
```

That's the whole configuration. You name `*api.Server` as a **root** — the thing you want — and
servo works backwards from there. It sees that `api.New` needs an `*OrderService`, that
`orders.New` needs a `*postgres.DB`, that `postgres.New` needs a `*config.Config`, and it builds
the chain. Anything nothing depends on simply isn't built.

Two things about that file are worth noticing, because they surprise people.

It never runs. It sits behind a build tag that excludes it from your binary. `servo generate` reads
it as *text* — it parses the syntax and never executes a line of it.

And it's the only file you maintain. Add a fourth package tomorrow, and as long as something in the
chain needs it, it gets wired. You don't come back and edit this.

Running `servo generate` writes `servo_gen.go` next to it. That file is plain Go. You can open it,
read it, set a breakpoint in it, and review it in a pull request like any other code. It's the
`main.go` you would have written by hand — except it's correct, and it stays correct when the graph
changes.

## Why this is more than a typing saving

If the only benefit were "fewer lines in main.go," this wouldn't be worth a tool. Three things
matter more.

**Mistakes move to build time.** A missing implementation, an ambiguous interface, a dependency
cycle — each one stops the build and points at a line. None of them can reach production, because
none of them can get past `go build`.

**Lifecycle comes free with the ordering.** This is the part that pays for itself. servo already
worked out the order to construct things in — which means it also knows the order to *start* them
in, and that shutdown is the same list reversed. So if your type has an `Init`, `Run`, `Stop`, or
`Health` method, it gets called at the right moment automatically. Startup runs in dependency order
and rolls back if a step fails. Shutdown runs backwards, under a time budget, and tells you about
anything that refused to stop instead of hanging forever waiting for it.

You don't register for any of this. A type qualifies by having the method. It never imports servo.

**Nothing is hidden.** No reflection, no registry, no `init()` functions running before `main`. The
generated file is the entire mechanism, and it's sitting in your repository where you can read it.

## What servo is not

It isn't a plugin system, a service locator, or a runtime container. You can't ask it for a
component by name while the program is running — there's no `Get[T]()`, because there's no
container there to ask. Everything is built once, at startup, and lives as long as the process.

It also has real boundaries, and some of them will rule it out for some projects. You can't have
two instances of the same type in one graph. You can't depend on "every implementation of this
interface." You can't generate a different graph for staging than for production.

Those aren't oversights — they follow from resolving everything before the program runs — but they
are limits, and you should know about them before you commit rather than after. They're all written
down in [Limitations](limitations.md).

## Where to go next

**If you have sixty seconds:** the [README](https://github.com/okian/servo) — install it, see three
constructors, one spec file, and the generated result.

**If you have an afternoon:** the [tutorial](tutorial/) builds a real order-management service from
an empty directory. Postgres, Redis, NATS, JWT auth, metrics, tracing, tests, CI, a container.
servo itself doesn't show up until chapter 11, and that's deliberate — the wiring problem is a lot
more convincing once you've got nine packages sitting there needing to be wired.

**If you're choosing between tools:** [How servo compares](comparison.md) puts it side by side with
`google/wire`, `uber-go/fx`, `uber-go/dig`, and writing it by hand — including the cases where one
of those is the better answer.

**If you need one specific answer:** the [reference](reference/) covers every command, every
marker, the lifecycle contract, the generated API, and every exported identifier — organised to be
looked up rather than read.

**If you're deciding whether to adopt it:** read [Limitations](limitations.md) first. It's the
shortest path to finding out this is the wrong tool for you, which is a genuinely useful thing for
a page to do.
