# Coming from wire or fx

**Who this is for:** anyone who knows `google/wire` or `uber-go/fx` (or `dig`, the container under
fx) and wants to know what each of their concepts becomes in servo — including the ones with no
servo equivalent, and what to write instead.

servo takes wire's model, constructors read at build time and plain Go written to disk, and adds
fx's lifecycle. Most of either vocabulary has a direct servo spelling. A few concepts have none, on
purpose, and for those this page says what to write. If you are still deciding whether to switch,
[How servo compares](comparison.md) is the page for that.

## At a glance

| wire | fx | servo |
| --- | --- | --- |
| `wire.NewSet(providers...)` | `fx.Provide(...)` | nothing: every constructor in your module is a candidate |
| an injector function calling `wire.Build` | the options passed to `fx.New` | a spec file calling `servo.Build`, behind `//go:build servoinject` |
| the injector's result | `fx.Invoke` | `servo.Root[T]()`, plus `Init` and `Run` methods for startup work |
| — | `fx.Lifecycle` hooks | `Init`, `Run`, `Drain`, `Flush` and `Stop` methods |
| a cleanup `func()` | `OnStop` | the same `(T, func(), error)` constructor shape |
| injector parameters | `fx.Supply` | `servo.Value[T]()` and `NewWith` |
| a `context.Context` injector parameter | — | a `context.Context` constructor parameter gets `New`'s `ctx` |
| `wire.Bind` | `fx.Annotate` with `fx.As` | implicit for one implementation; `servo.Bind[I, C]()` for several |
| — | `fx.Replace` in a test | `servo.Override[I, C]()`, then `NewTestApp` or `NewTestAppWith` |
| — | `fx.Decorate` | `servo.Trace[I]()` for spans; otherwise a wrapper type and a `Bind` |
| a `wire.NewSet` several injectors share | `fx.Module` | `servo.Include(fn)`, which shares markers, not constructors |
| — | `fx.Private` | Go's own visibility rules |
| distinct types | `name:"..."` tags | distinct types |
| — | `group:"..."` value groups | no equivalent: one constructor that builds the slice |
| `wire.Struct` | `fx.In` | no equivalent: write the constructor |
| `wire.FieldsOf` | `fx.Out` | no equivalent: one small constructor per field |
| `wire.Value`, `wire.InterfaceValue` | — | a constructor, a `servo.Value`, or a `//servo:config` default |
| — | `optional:"true"` | no equivalent: a no-op implementation |
| — | `fx.Shutdowner` | a `Run` method that returns an error |
| `wire check`, `wire diff` | `fx.ValidateApp` | `servo generate`, and `servo check` in CI |
| — | `fxtest.New`, `fx.Populate` | `NewTestApp`, in the injector's own package |

The rest of the page takes the rows in that order.

## The shape of the move

From wire, the files line up almost one for one. An injector file gated `//go:build wireinject`
becomes a spec file gated `//go:build servoinject`; `wire.Build(...)` inside an injector function
becomes `servo.Build(...)` inside any function at all, since servo reads the call and never runs it;
`wire_gen.go` becomes `servo_gen.go`; `wire gen ./...` becomes `go tool servo generate`, which
handles every injector under `--dir`; and `wire diff` in CI becomes `servo check`. Two things differ.
servo writes its `go:generate` directive into a separate untagged file, `servo_generate.go`, where
wire puts its own into `wire_gen.go`. And the generated file is not the function you declared: it
is an `App` type with `Run`, `Shutdown`, `Health`, `Ready`, `Graph` and `Report` methods, and a
`New` that builds it.

From fx, the options you pass to `fx.New` move out of `main` into the spec file, and `main` shrinks
to one line:

```go
// fx
func main() {
	fx.New(
		fx.Provide(logger.New, postgres.New, api.New),
		fx.Invoke(func(*api.Server) {}),
	).Run()
}
```

```go
// servo: cmd/app/spec.go
//go:build servoinject

package main

import (
	"example.com/app/api"
	"github.com/okian/servo/v3/servo"
)

func wire() {
	servo.Build(
		servo.Root[*api.Server](),
	)
}
```

```go
// servo: cmd/app/main.go
package main

import "github.com/okian/servo/v3/servo"

func main() { servo.Main(New) }
```

`servo.Main` plays the part of `fx.App.Run`: it cancels the context on SIGINT, SIGTERM or SIGHUP,
shuts down under a deadline, and exits non-zero when startup, `Run` or the shutdown fails. The
[README's Quick start](https://github.com/okian/servo#quick-start) builds that program from an
empty module, and [Your main](reference/lifecycle.md#your-main) documents each step.

## Provide is implicit

servo has no `Provide` and no provider set. Every top-level function in your module that looks like
a constructor is a candidate: exported (or unexported in the injector's own package), returning
`T`, `(T, error)`, `(T, func())` or `(T, func(), error)`, where `T` is a named type, a pointer to
one, or an interface with methods. [Resolution rules](reference/resolution.md#what-counts-as-a-provider)
has the exact list. What gets built is what the roots reach; a constructor nothing reaches costs
nothing.

So the list you maintained goes away. Its other job, choosing between two constructors of the same
type, moves into the type system: two functions returning one type are an ambiguity, and the fix
is two types (see [Names](#names-become-types)), not a narrower list.

A constructor in your module wins over the standard library's and your dependencies' constructors
of the same type, so a module that `fx.Provide`d its own `*sql.DB` keeps doing so with an ordinary
function. `*sql.DB` has `Close` rather than servo's `Stop(ctx) error`, so hand the close back as a
cleanup func:

```go
func NewDB(cfg config) (*sql.DB, func(), error) {
	db, err := sql.Open("pgx", cfg.dsn)
	if err != nil {
		return nil, nil, err
	}
	return db, func() { db.Close() }, nil
}
```

## Invoke becomes Root, plus Init and Run

`fx.Invoke` does two jobs: it forces construction of something nothing else depends on, and it
runs code at startup. servo splits them. `servo.Root[T]()` names what to build. Startup work is a
method on a type: `Init(ctx) error` runs inside `New`, in dependency order, and a failure rolls
back everything built so far; `Run(ctx) error` runs for the life of the process, inside `Run`.

`Init`'s context belongs to the call and is cancelled when `Init` returns, so a goroutine started
on it dies with it. Long-lived work goes in `Run`.

## Lifecycle hooks become methods

A constructor in fx takes `fx.Lifecycle` and appends hooks; a type in servo has methods, found by
their signatures, with no registration and no servo import:

```go
// fx
func NewServer(lc fx.Lifecycle, s store.Store) *Server {
	srv := &Server{http: &http.Server{Addr: ":8080", Handler: handler(s)}}
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ln, err := net.Listen("tcp", srv.http.Addr)
			if err != nil {
				return err
			}
			go srv.http.Serve(ln)
			return nil
		},
		OnStop: srv.http.Shutdown,
	})
	return srv
}
```

```go
// servo
func NewServer(s store.Store) *Server {
	return &Server{http: &http.Server{Addr: ":8080", Handler: handler(s)}}
}

func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	go s.http.Serve(ln)
	<-ctx.Done()
	return nil
}

func (s *Server) Stop(ctx context.Context) error { return s.http.Shutdown(ctx) }
```

An `OnStart` hook must not block, which is why it starts a goroutine; servo's `Run` *is* that
goroutine, and blocks until its context is done. `OnStop` becomes `Stop`, and servo has two earlier
stop phases, `Drain` (stop taking new work) and `Flush` (push out what is buffered), for the
components that need them. fx runs start hooks in the order they were appended and stop hooks in
reverse; servo derives the order from the graph — `Init` level by level, concurrently within a
level, and shutdown in reverse
dependency order, each stop phase under its own budget, with a [`servo.Report`](reference/servo-package.md#report)
saying what stopped, what failed and what was abandoned. The
[lifecycle reference](reference/lifecycle.md) has the whole contract.

wire's cleanup functions keep their shape. A constructor returning `(T, func(), error)` has its
cleanup called last in that node's stop sequence, during `Shutdown` and during a startup rollback
alike.

## Supply and injector parameters become Value

A value the program only has once it is running — parsed flags, a version string from `-ldflags`
— is `fx.Supply` in fx and an injector parameter in wire. In servo it is declared in the spec, and
`New` gains a sibling, `NewWith`, taking a `Values` struct with one field per declaration:

```go
servo.Build(
	servo.Root[*api.Server](),
	servo.Value[conf.Flags](),
)
```

```go
func main() {
	flags := conf.Parse(os.Args[1:])
	servo.Main(func(ctx context.Context) (*App, error) {
		return NewWith(ctx, Values{Flags: flags})
	})
}
```

A supplied value beats any constructor of the same type. It is supplied once per app, not per
call, and `NewWith` refuses a nil pointer, interface, map, func or chan, naming the field, rather
than handing a nil to a constructor. [`Value`](reference/spec.md#value) has the details.

One input needs no declaration. wire hands an injector's `context.Context` parameter to any
provider asking for one; in servo, a constructor parameter of type `context.Context` receives the
`ctx` that `New`, `NewWith` or `NewTestApp` was called with. It is a construction-time context,
for a dial or a ping, not for work that outlives the call — and a scoped constructor, which runs
inside `Acquire` after `New` has returned, cannot take one; it gets its context in `Init` instead.

## Binding an interface: usually nothing, sometimes Bind

```go
// fx
fx.Provide(fx.Annotate(postgres.New, fx.As(new(store.Store))))

// wire
wire.Bind(new(store.Store), new(*postgres.DB))
```

In servo a parameter of interface type is matched structurally: if exactly one type in your module
implements `store.Store`, it is used, with nothing declared. With several, generation stops with
`ambiguous provider for store.Store` and prints the lines to paste, one per implementation:

```go
servo.Bind[store.Store, *postgres.DB](),
```

Two differences from the originals. `fx.As` replaces the provided type, so `*postgres.DB` itself is
no longer available unless you add `fx.Self()`; a servo `Bind` redirects the interface and leaves
`*postgres.DB` a node, so consumers of either get the same instance. And wire needs a `Bind` for
every interface it injects, where servo needs one only to choose. An explicit `Bind` always wins,
so it also works as a deliberate override of an unambiguous match.

## Replace becomes Override, in tests

`fx.Replace` swaps a value in the graph, most often in an `fxtest` app. servo's equivalent is
declared in the spec and applies only to tests:

```go
servo.Bind[store.Store, *postgres.DB](),         // production
servo.Override[store.Store, *mockstore.Store](), // tests
```

One `Override` makes `servo generate` write a second file, `servo_gen_test.go`, with `NewTestApp`:
the same graph with each overridden interface built by the override's constructor. An override
applies to the whole graph, never to one consumer.

For a mock built per test — a gomock controller holding the test's own `*testing.T`, say —
`NewTestAppWith` takes a `TestValues` struct with a field per `Override`, typed as the interface.
Set, the test's instance is used as that node, with no constructor and no lifecycle calls; left
nil, the override's constructor builds it as before. That is what lets tests that each supply their
own mock run in parallel. [The test variant](reference/generated-api.md#the-test-variant) has the
rules, including which overrides get a field.

To replace something in production rather than in tests, use `Bind`.

## Decorate becomes Trace, or a wrapper and a Bind

servo generates one kind of decorator: `servo.Trace[I]()` wraps whatever `I` resolves to in an
OpenTelemetry span per method, for every consumer of `I`.
[Traced interfaces](reference/tracing.md) covers it.

Any other decoration is a type you write: a wrapper whose constructor takes the concrete type it
wraps, and a `Bind` sending the interface to the wrapper. The tutorial's circuit breaker is exactly
this ([chapter 17](tutorial/17-resilience.md)):

```go
func NewCircuitBreakerCache(next *redis.Cache) *CircuitBreakerCache { ... }
```

```go
servo.Bind[cache.OrderCache, *resilience.CircuitBreakerCache](),
```

The wrapper must ask for `*redis.Cache`, not `cache.OrderCache`: the `Bind` would answer the
interface with the wrapper itself, a cycle. The wrapped type is still built and keeps its own
`Init` and `Stop`.

## Module becomes Include, for markers

`servo.Include(fn)` is the reusable bundle: a function returning `[]servo.Marker`, spliced into
every `Build` that names it, so several binaries share their wiring.

```go
//go:build servoinject

package wiring

func Shared() []servo.Marker {
	return []servo.Marker{
		servo.Bind[store.Store, *postgres.DB](),
		servo.Bind[cache.Cache, *redis.Cache](),
	}
}
```

```go
servo.Build(
	servo.Include(wiring.Shared),
	servo.Root[*api.Server](),
)
```

It carries markers — `Bind`, `Override`, `Scoped`, `Value`, `Trace`, `ConfigFile` — and not
constructors, because constructors need no listing. A marker written after the `Include` overrides
the included one. Two things an `fx.Module` or a wire set does have no counterpart: fx scopes
`Decorate`, `Replace` and `Private` to a module, and a wire set decides which constructors are
available at all. [`Include`](reference/spec.md#include) has the rules.

## Private is Go's visibility

There is no marker. A constructor is a candidate only where the injector could call it: an
unexported one only for an injector in its own package, and one under another tree's `internal/`,
or in a package that imports the injector's own, never — servo sets it aside for that injector and
says why if it was the only provider.

## Names become types

fx tells two values of one type apart by a string — a `name:"ro"` tag, set with `fx.ResultTags` on
the provider and read with `fx.ParamTags` or an `fx.In` field on the consumer. servo has no names or
tags: a value's identity is its type. Two instances of one type become two types, which is also wire's answer:

```go
type Primary struct{ *sql.DB }
type Replica struct{ *sql.DB }

func NewPrimary(cfg primaryConfig) (*Primary, error) { ... }
func NewReplica(cfg replicaConfig) (*Replica, error) { ... }

func NewOrders(rw *Primary, ro *Replica) *Orders { ... }
```

Embedding keeps every `*sql.DB` method available on both, and the compiler checks the name a
string tag only checked at startup. The [README](https://github.com/okian/servo#multiple-instances-of-the-same-type)
has a complete, generated example.

## Value groups: no equivalent

There is no way to ask for every implementation of an interface. Write one constructor that takes
each of them and returns a named slice type:

```go
type Plugins []Plugin

func NewPlugins(a *audit.Plugin, b *billing.Plugin) Plugins { return Plugins{a, b} }
```

A named slice type is an ordinary named type, so `NewPlugins` is a provider like any other and
consumers take `Plugins`. The list is maintained by hand, which is the price of every edge being
visible in a signature. Two of fx's common uses for groups have their own mechanisms in servo's
generated HTTP layer: middleware is attached with `servo.Use` in the spec, in declared order, and
handlers are `//servo:` route directives, found wherever they are in the module.
[HTTP routes](reference/http.md) covers both.

## In and Struct: write the constructor

`fx.In` parameter structs and `wire.Struct` field filling have no equivalent. A constructor's
parameters are the dependency list, and they are what servo reads; a struct with twelve
dependencies gets a constructor with twelve parameters. (A struct parameter would itself be a node
of the graph, needing a constructor of its own.)

## Out and FieldsOf: one constructor per field

`fx.Out` result structs and `wire.FieldsOf` provide several values from one constructor, and a
servo provider returns exactly one. Build the bundle with one constructor and hand out each field
with another:

```go
func NewClients(cfg config) (*Clients, error) { ... }

func NewOrdersClient(c *Clients) *OrdersClient { return c.Orders }
func NewAuditClient(c *Clients) *AuditClient   { return c.Audit }
```

## Value and InterfaceValue: a constructor, a Value or a config

`wire.Value(expr)` binds an expression. servo reads its spec file and never runs it, so there is no
marker holding an expression. A fixed value is a constructor returning it; a value the program
computes is a `servo.Value`; a setting with a sensible default is a [`//servo:config`](reference/config.md)
field with `default=`.

## Optional dependencies: no equivalent

`optional:"true"` has no counterpart: every parameter must resolve, and a missing provider fails
generation. Provide an implementation that does nothing, and bind it in the builds that lack the
real one — a [build variant](reference/cli.md#build-variants) is how one injector holds both — or
have the constructor take a config and decide inside.

## Shutdowner: return an error from Run

A `Run` method that returns an error cancels the context every other runner was given, `Run`
returns it, and `servo.Main` then shuts the app down and exits 1. A runner that returns `nil`
cancels nothing.

## Validation is generation

`fx.ValidateApp` in a test, and `wire check`, are both a separate step you remember to run. In servo
the check is generation itself: a missing provider, an ambiguity or a cycle stops `servo generate`
with the file and line that caused it. In CI, `servo check` fails when a committed generated file
no longer matches the code or no longer compiles. [servo in CI](servo-in-ci.md) has the workflow.

## Tests: NewTestApp

`fxtest.New(t, ...)` with `fx.Populate(&x)` becomes `NewTestApp(ctx)` (or `NewTestAppWith`) in a
test in the injector's own package, where the app's unexported fields are the components — no
populate step. [`servotest`](reference/servotest-package.md) adds leak checks and order assertions,
and `servotest.NodeName[T]()` spells a type the way the report names its node. For a single
component, skip the graph: call its constructor with fakes, as you would in either tool.

## dig

Everything above for fx applies to dig, the container fx is built on: `Provide` is implicit,
`Invoke` is `Root`, `dig.In` and `dig.Out` have no equivalent, and names and groups map as they do
for fx. dig has no lifecycle, so servo's methods replace whatever start and stop code you wrote
around the container.
