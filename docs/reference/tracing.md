# Traced interfaces

**Who this is for:** anyone who wants a span around every call into a dependency without
instrumenting the dependency, and anyone deciding whether `servo.Trace` fits their graph.

A [`servo.Trace[I]()`](spec.md#trace) marker makes `servo generate` emit an OpenTelemetry
wrapper for interface `I` and hand that wrapper to every consumer of `I`, in place of whatever `I`
would otherwise resolve to. The wrapper is plain Go in the injector package: a type implementing
`I` whose every method with a leading `context.Context` runs inside a span, passes the span's
context on to the implementation, and records a non-nil error on the span. Nothing in the
runtime is involved — the `servo` package has no OpenTelemetry dependency, and neither does the
generator's module. The generated file imports `go.opentelemetry.io/otel/trace` and
`go.opentelemetry.io/otel/codes` in *your* module, exactly as a config file's decoder is yours.

```go
servo.Build(
	servo.Root[*api.Server](),
	servo.Trace[repository.OrderRepository](servo.Expected(domain.ErrNotFound)),
	servo.Trace[cache.OrderCache](servo.Expected(cache.ErrMiss)),
	servo.Trace[broker.EventPublisher](),
)
```

With a request span opened by a tracing middleware — [`servo.Use`](http.md) on an emitted server,
or your own handler chain — every traced dependency the request touches becomes a child span, with
no instrumentation written by hand and nothing a refactor can forget to update: the wrapper is
regenerated from the interface's current method set every time.

## What can be traced

**A named interface, with at least one method.** A consumer holding a concrete type has no seam to
wrap — the wrapper would be a different type, and the consumer's parameter says which type it
holds — so `servo.Trace[*postgres.DB]()` is a declaration-time error pointing at the interface the
consumer should depend on instead. This is the one rule that gives the feature its shape, and it
is the same advice the rest of servo gives: depend on the interface where more than one
implementation could stand behind it.

**Every method must be one the injector package can implement.** An unexported method declared in
another package cannot be named from the generated file, and neither can an unexported type an
exported method's signature mentions — `Get(ctx, opts ...option)` is fine for a consumer and
impossible for a wrapper that has to spell the signature out — so an interface carrying either is
refused unless it is declared in the injector's own package.

**Not the OpenTelemetry API itself**, which is what every wrapper is built from. And not a
[scope accessor](scopes.md) or a scope's key type: generated code and extracted values
respectively, with no provider behind them to wrap.

## What the wrapper does

Everything about the wrapper follows from the interface's method set, so it is worth knowing the
rules rather than reading the file each time:

| A method whose… | Gets |
| --- | --- |
| first parameter is `context.Context` | A span named `<pkg>.<Iface>/<Method>`, started from the incoming context; the call receives the span's context |
| last result is `error`, and the call returns a non-nil one | `span.RecordError(err)` and `span.SetStatus(codes.Error, err.Error())` — unless the error is [expected](#expected-errors) |
| first parameter is anything else, or there are no parameters | A plain delegation — no span, no context, nothing recorded |

A span ends when the method returns, panics included. The instrumentation scope passed to
`TracerProvider.Tracer` is the interface's full import path, so two packages with one short name
stay distinguishable in a backend even though their span names both read `store.Store/Get`.

The wrapper has **no lifecycle of its own**. The implementation behind it keeps its `Init`, `Run`,
`Stop` and the rest, called by servo exactly as before, because the wrapper is a node without
[capabilities](lifecycle.md) by construction — not by structural detection, which an interface
that happened to declare an `Init` method would otherwise trip. Nothing is initialised twice and
nothing is stopped through a span.

Here is the whole of one generated wrapper, for an interface with a traced method and an
untraced one:

```go
type tracedStore struct {
	inner  store.Store
	tracer trace.Tracer
}

func newTracedStore(inner store.Store, tp trace.TracerProvider) *tracedStore {
	return &tracedStore{inner: inner, tracer: tp.Tracer("example.com/app/store")}
}

func (t *tracedStore) Get(ctx context.Context, key string) (string, error) {
	ctx, span := t.tracer.Start(ctx, "store.Store/Get")
	defer span.End()
	r0, err := t.inner.Get(ctx, key)
	if err != nil && !errors.Is(err, store.ErrMissing) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return r0, err
}

func (t *tracedStore) Close() error {
	return t.inner.Close()
}
```

Parameter names are the interface's own where they are usable, so the wrapper reads like the
interface it implements; a blank, missing, or clashing name becomes a positional `p1`, `p2`. Only
the stable OpenTelemetry API is used — `TracerProvider`, `Tracer.Start`, `Span.End`,
`Span.RecordError`, `Span.SetStatus`, `codes.Error` — and nothing from a versioned `semconv`
package, so the generated file has no version to chase.

## Expected errors

Go spells a cache miss, a not-found and an end of stream as errors. A span marked failed for every
one of them makes any error rate computed from traces meaningless, so `servo.Trace` takes an
option naming the errors that are results rather than failures:

```go
servo.Trace[cache.OrderCache](servo.Expected(cache.ErrMiss), servo.Expected(cache.ErrStale)),
```

The argument must name a **package-level variable or constant of an error type** — exported, or
declared in the injector's own package. It is read as syntax, exactly as `servo.Include` reads a
function's name and never calls it: the generated check is `errors.Is(err, cache.ErrMiss)`, so
the name is what servo needs and the value is never evaluated. A call, a literal, a local, a
non-error, or an unexported sentinel from another package is refused at the declaration. An error
that matches an `Expected` one ends its span cleanly, with nothing recorded.

There is no option for wrapping without error status at all, and no filter by error *type*. If a
method's errors are routinely not failures, the method's signature is the place to say so.

## The tracer provider

Every wrapper takes `go.opentelemetry.io/otel/trace.TracerProvider` as an ordinary graph
dependency, resolved like any constructor parameter — and resolved **among your own module's
constructors only**. The OpenTelemetry packages return that interface from several functions of
their own (the global getter, the no-op provider), none of which is the one you meant, so for this
one key the usual exact-type step ignores candidates from other modules. There is no fallback to
`otel.GetTracerProvider()`: a global is what makes two apps in one process collide, and an
"initialise tracing before anything else" ordering assertion is exactly the kind of claim servo
derives from dependencies rather than trusts a comment for.

Write a constructor whose result implements the interface. The SDK's `*sdktrace.TracerProvider`
does; so does a type of your own that embeds it, which is the shape that also gives servo a `Stop`
to flush and shut the exporter down at the right point in the teardown:

```go
type Tracer struct{ *sdktrace.TracerProvider }

func NewTracer(cfg Config) (*Tracer, error) {
	// exporter, resource, sampler...
	return &Tracer{TracerProvider: sdktrace.NewTracerProvider(opts...)}, nil
}

func (t *Tracer) Stop(ctx context.Context) error { return t.Shutdown(ctx) }
```

One such type is the sole implementation and auto-binds. Two are an ambiguity naming the
`servo.Bind[trace.TracerProvider, …]()` to add, and an [`Override`](spec.md#override) can swap in
an in-memory provider for the test variant. A module in which nothing imports the trace API at all
gets a diagnostic naming that import rather than a wrapper written against a package the build
cannot see.

## Where the wrapper sits

The wrapper is a node in the graph: constructed in `New` after the implementation and the tracer
provider, before its consumers, at one level above the deeper of its two dependencies. It appears
everywhere a node does — `servo explain`, `servo why`, `servo graph`, the generated header and
[`App.Graph()`](generated-api.md#graph) — keyed as the interface's type string with a `#traced`
suffix, with binding `traced`, and with [`Wraps`](servo-package.md#graph-and-graphnode) naming
the node it decorates:

```
$ servo explain repository.OrderRepository
example.com/app/internal/repository.OrderRepository#traced
  provider:     generated OpenTelemetry wrapper around *example.com/app/internal/repository/postgres.Store (cmd/api/spec.go:21:3)
  binding:      traced
  lifetime:     singleton — one per process, built by New
  level:        3
  depends on:   *example.com/app/internal/repository/postgres.Store, *example.com/app/internal/observability.Tracer
  depended on:  *example.com/app/internal/service.OrderService
  capabilities: none
```

Three consequences of it being a real node:

**It wraps whatever the interface resolves to.** An explicit `Bind`, a structural match, a supplied
[`Value`](spec.md#value), or — in the test variant — an `Override`: the interface resolves exactly
as it would untraced, and the wrapper sits around the answer. `NewTestApp` traces the mock through
a wrapper of its own, so the generated code is exercised by the same tests that exercise the
mock, and an `Override[trace.TracerProvider, …]` lets those tests capture the spans.

**A consumer of the concrete type bypasses it.** A constructor taking `*postgres.Store` directly
gets the store, not the wrapper — the same node the wrapper holds. Tracing is a property of the
interface edge, and `servo explain` on the concrete type shows which consumers take which.

**Two traced interfaces behind one implementation are two wrappers** around one node, each with
its own span names. `repository.OrderRepository` and `repository.UserRepository` both bound to
`*postgres.Store` is the ordinary case.

A scoped member taking a traced *singleton* interface borrows the wrapper the way it borrows the
logger behind it. What is not supported is tracing an interface whose implementation is itself
scoped: a per-key wrapper would have to be built and torn down inside the scope's entry, which is
well-defined and not yet generated, so it is a diagnostic rather than a silent singleton. See
[Limitations](../limitations.md#a-scoped-implementation-cannot-be-traced).

## Diagnostics

| Message | Cause | Fix |
| --- | --- | --- |
| `servo.Trace's type argument must be an interface, not *postgres.DB` | A concrete type | Make the consumer depend on the interface, then trace that |
| `servo.Trace's type argument must be a named interface` | An interface literal | Name it; the span names come from the name |
| `servo.Trace[I]: the interface declares no methods` | `any`, or an empty interface | Nothing to wrap |
| `servo.Trace[I]: method m is unexported, and the wrapper is generated into package main` | An unexported method from another package | Export it, or declare the interface beside the spec |
| `servo.Trace[I]: method M's signature uses store.option, an unexported type the wrapper … cannot name` | An exported method mentioning an unexported type from another package | Export the type, or declare the interface beside the spec |
| `servo.Trace[I]: the OpenTelemetry trace API is what every wrapper is built from` | Tracing `trace.TracerProvider` or `trace.Tracer` | Trace your own interfaces |
| `servo.Trace[I] declared twice` | Two local declarations for one interface | Keep one; a local one may still supersede an included one |
| `servo.X is not a servo.Trace option` | Anything but `Expected` inside the call | The only option is `servo.Expected(err)` |
| `servo.Expected belongs inside servo.Trace[I](...)` | `Expected` at `Build`'s top level | Move it into the `Trace` call |
| `servo.Expected's argument must name a package-level error variable or constant` | A literal, a call, a local, a field | Name the sentinel |
| `servo.Expected(X): X is a int, not an error` | A non-error at package level | Name an error |
| `servo.Expected(X): X is unexported, and the generated errors.Is check lives in package main` | An unexported sentinel from another package | Export it |
| `servo.Expected(X) declared twice in the same servo.Trace` | The same sentinel named twice | Once is enough |
| `servo.Trace[I]() is declared, but nothing in the graph depends on I` | No consumer requests the interface — checked per generated file, so an `Override` can make this true of the test variant alone | Take it as a constructor parameter the roots reach, or delete the declaration |
| `servo.Trace[I] needs go.opentelemetry.io/otel/trace, which nothing in this module's build imports` | No package in the build imports the trace API | Write the tracer provider's constructor; its import brings the package in |
| `no provider for go.opentelemetry.io/otel/trace.TracerProvider` | No constructor in your module produces one | Write it, as [above](#the-tracer-provider) |
| `servo.Trace[I]: I is a scope accessor interface` | Tracing a `Scoped` declaration's accessor | Trace the interfaces the scoped type depends on |
| `servo.Trace[I]: I resolves to *chat.Room, which is scoped` | The implementation is a scope member | Not supported in this release; leave it untraced |

The resolution ones carry the same "needed by" chain every other resolution diagnostic does, with
the wrapper reading as `the servo.Trace wrapper for I` at the declaration's position.

## What `servo.Trace` does not do

No metrics: a per-method duration histogram and error counter would take a `metric.MeterProvider`
through the same wrapper, and is a separate marker if it comes, because the two signals have
different providers and are opted into separately. No logs. No attributes beyond the span name
and the recorded error — in particular no argument values, which could be anything from a
password to a megabyte. No tracing of concrete-typed edges, by design, and no wildcard: each
traced interface is a line in the spec, shared across injectors through
[`servo.Include`](spec.md#include) when the list gets long.
