[![Go](https://github.com/okian/servo/actions/workflows/go.yml/badge.svg)](https://github.com/okian/servo/actions/workflows/go.yml)
[![Tutorial](https://github.com/okian/servo/actions/workflows/tutorial.yml/badge.svg)](https://github.com/okian/servo/actions/workflows/tutorial.yml)
[![CodeQL](https://github.com/okian/servo/actions/workflows/codeql-analysis.yml/badge.svg)](https://github.com/okian/servo/actions/workflows/codeql-analysis.yml)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/14271/badge)](https://www.bestpractices.dev/projects/14271)
[![codecov](https://codecov.io/gh/okian/servo/branch/master/graph/badge.svg)](https://codecov.io/gh/okian/servo)
[![Go version](https://img.shields.io/github/go-mod/go-version/okian/servo)](go.mod)
[![License: MIT](https://img.shields.io/github/license/okian/servo)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/okian/servo/v3.svg)](https://pkg.go.dev/github.com/okian/servo/v3)
[![Docs](https://img.shields.io/badge/docs-okian.github.io%2Fservo-00ADD8)](https://okian.github.io/servo/)

# servo v3

Servo is a build-time code generator for Go. It reads your constructors, resolves the
application's object graph from their signatures, and writes plain Go source that constructs,
starts, supervises and shuts the application down in dependency order. Declare routes as
`//servo:` comments and it writes the HTTP servers that serve them, too.

No reflection. No runtime registry. No `init()`. No hand-written wiring.

The generated file is ordinary Go: compiler-checked, IDE-navigable, steppable in a debugger, and
readable by a human at 3am.

**Documentation: [okian.github.io/servo](https://okian.github.io/servo/).** The
[preface](https://okian.github.io/servo/preface.html) explains dependency injection from scratch,
[how servo compares](https://okian.github.io/servo/comparison.html) weighs it against wire, fx and
dig, and [limitations](https://okian.github.io/servo/limitations.html) is the page to read before
adopting it. The [reference](https://okian.github.io/servo/reference/) documents every command and
flag, every marker, the resolution rules, every diagnostic, the lifecycle contract, the generated
API, and every exported identifier in `servo` and `servotest`.
[ARCHITECTURE.md](ARCHITECTURE.md) explains how the load, scan, resolve and emit pipeline fits
together, and [CHANGELOG.md](CHANGELOG.md) records what changed and how releases are versioned. To
learn servo by building something, [docs/tutorial/](docs/tutorial/README.md) builds a complete
microservice (Postgres, Redis, NATS, JWT auth, OpenTelemetry, CI/CD) layer by layer, against the
separate runnable module in [`examples/tutorial`](examples/tutorial).

v3 is a from-scratch rewrite and shares no API with the releases before it. Those (informally v1;
`/v2` was only the import path of their last breaking change) were a runtime lifecycle sequencer
built on a global registry and a hand-maintained order. Dependencies are now declared by
constructor parameters alone, and resolution happens at build time. `servo migrate` reads v1 code
and prints a v3 skeleton.

## Quick start

In a Go module (`go mod init example.com/app` if you are starting from nothing), add the generator
as a tool, once per module:

```
go get -tool github.com/okian/servo/v3/cmd/servo
```

That records servo in `go.mod` beside your other requirements and makes every command below
available as `go tool servo`, so a developer and CI generate with the same version. It is
`go tool servo`, not `go run github.com/okian/servo/v3/cmd/servo`: a module that requires servo for
its marker package alone does not have the generator's own dependencies in its build list, and
`go run` fails on a missing `go.sum` entry.
[Installing](https://okian.github.io/servo/reference/cli.html#installing) has the details, and
`go install github.com/okian/servo/v3/cmd/servo@latest` gives you an unpinned `servo` on `PATH`
for interactive use. Every [release](https://github.com/okian/servo/releases) also attaches
`servo` and `servo-vet` binaries for Linux, macOS and Windows, with checksums and a signed
build-provenance attestation; they still need `go` on `PATH`, since servo loads your module through
the go command. [RELEASING.md](RELEASING.md) has the rest.

Write ordinary constructors. None of these files imports servo:

```go
// logger/logger.go
package logger

import (
    "log"
    "os"
)

type Logger struct{ *log.Logger }

func New() *Logger { return &Logger{log.New(os.Stderr, "", 0)} }
```

```go
// store/store.go
package store

type Store interface{ Get(key string) string }
```

```go
// memstore/memstore.go
package memstore

import (
    "context"

    "example.com/app/logger"
)

type Store struct {
    log  *logger.Logger
    data map[string]string
}

func New(log *logger.Logger) (*Store, error) {
    return &Store{log: log, data: map[string]string{"hello": "world"}}, nil
}

func (s *Store) Get(key string) string { return s.data[key] } // satisfies store.Store

func (s *Store) Init(ctx context.Context) error   { s.log.Print("memstore: open"); return nil }
func (s *Store) Stop(ctx context.Context) error   { s.log.Print("memstore: closed"); return nil }
func (s *Store) Health(ctx context.Context) error { return nil }
```

```go
// api/api.go
package api

import (
    "context"
    "fmt"
    "net"
    "net/http"
    "time"

    "example.com/app/logger"
    "example.com/app/store"
)

type Server struct {
    srv *http.Server
    log *logger.Logger
}

// New depends on the interface, not on memstore directly.
func New(s store.Store, log *logger.Logger) *Server {
    h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintln(w, s.Get(r.URL.Path[1:]))
    })
    srv := &http.Server{Addr: "localhost:8080", Handler: h, ReadHeaderTimeout: 5 * time.Second}
    return &Server{srv: srv, log: log}
}

// Run serves until ctx is cancelled. A port already in use is Run's error.
func (s *Server) Run(ctx context.Context) error {
    ln, err := net.Listen("tcp", s.srv.Addr)
    if err != nil {
        return err
    }
    s.log.Print("api: listening on ", ln.Addr())
    go s.srv.Serve(ln) // returns once Stop shuts the server down
    <-ctx.Done()
    return nil
}

// Stop drains in-flight requests; Shutdown calls it before memstore's Stop.
func (s *Server) Stop(ctx context.Context) error {
    s.log.Print("api: draining")
    return s.srv.Shutdown(ctx)
}
```

`Init`, `Run`, `Stop` and `Health` are [capabilities](#capabilities): servo calls each one because
the type has the method, never because anything registered for it.

Scaffold the injector, the package the generated code will live in:

```
go tool servo init --dir cmd/app
```

It writes three files: `cmd/app/servo_spec.go`, the spec, behind the `servoinject` build tag (the
examples in this repository name theirs `spec.go`); `cmd/app/servo_generate.go`, which holds the
`go:generate` directive; and, since `cmd/app` has no `main.go` yet, `cmd/app/main.go`. Declare the
roots in the spec. Everything they transitively depend on is what gets built, and nothing else is:

```go
//go:build servoinject

package main

import (
    "example.com/app/api"
    "example.com/app/memstore"
    "example.com/app/store"
    "github.com/okian/servo/v3/servo"
)

func wire() {
    servo.Build(
        servo.Root[*api.Server](),
        servo.Bind[store.Store, *memstore.Store](), // optional with one implementation; required with two
    )
}
```

The other two files are complete as written:

```go
// cmd/app/servo_generate.go (no build tag, deliberately)
package main

//go:generate go tool servo generate
```

```go
// cmd/app/main.go
package main

import "github.com/okian/servo/v3/servo"

func main() { servo.Main(New) }
```

The directive gets its own untagged file because `go generate` honours build constraints: behind
the inactive `servoinject` tag it would never run, and `go generate ./...` would exit 0 having
generated nothing.

Generate, and run:

```
$ go generate ./...
$ go run ./cmd/app
memstore: open
api: listening on 127.0.0.1:8080
```

From another terminal, `curl localhost:8080/hello` prints `world`. Back in the first, press Ctrl+C:

```
^C
api: draining
memstore: closed
```

`go generate` wrote `cmd/app/servo_gen.go` next to the spec file, containing `New`, `Run`,
`Shutdown`, `Health`, `Ready`, `Graph` and `Report`: construction in dependency order, lifecycle
calls only for the capabilities a type actually implements, and reverse-order shutdown with budgets
and a per-node report. The output above is that order. The store opens before the server listens,
and the server drains before the store closes. A spec declaring `servo.HTTP(...)` additionally
gets one emitted server per listener group; see [HTTP routes](#http-routes).

`servo.Main` is the whole of `main`, so `main.go` never grows with the graph. It cancels the
context `New` and `Run` receive on SIGINT, SIGTERM or SIGHUP, runs `Shutdown` under a deadline of
its own, exits at once on a second signal, logs every failure at ERROR, and exits non-zero when
`New` fails, `Run` fails, or the shutdown report is not clean.
[Lifecycle](https://okian.github.io/servo/reference/lifecycle.html) documents each step, and the
`main` to write by hand when you need more control.

[`examples/basic`](./examples/basic) is the complete, runnable version, with a genuinely ambiguous
interface binding, an error-returning constructor with rollback, and every lifecycle capability.
If you already know wire or fx,
[Coming from wire or fx](https://okian.github.io/servo/coming-from-wire-and-fx.html) maps each of
their concepts onto servo's.

## Interfaces vs. concrete types

A dependency's declared type decides how it resolves. There is no mode to opt into:

- **An interface parameter** (`func New(s store.Store) *Server`): servo scans every candidate in
  scope for the one whose result type implements it, checked at generation time with
  `types.Implements`. Exactly one implementation auto-binds. Zero is a missing-provider error, and
  two or more is an ambiguity, resolved by adding `servo.Bind[store.Store, *postgres.DB]()`.
- **A concrete parameter** (`func New(l *logger.Logger) *DB`): servo looks for the one function
  that returns exactly that type. `Bind` does not apply, and the only thing to disambiguate is a
  second function returning the same type (see
  [Multiple instances of the same type](#multiple-instances-of-the-same-type)).

An explicit `Bind` wins even over an unambiguous match, so it also works as a deliberate override.
Declare an interface where a component should accept any of several implementations, and a
concrete type where there is exactly one. Servo follows whichever you wrote and never converts one
into the other. Full rules: [Resolution](https://okian.github.io/servo/reference/resolution.html).

## Values the caller supplies

Both rules above end at some function's result. A value that only exists once the process is
running, such as a parsed flag set, a version string set with `-ldflags`, or a DSN assembled from
the environment, has no such function, and a package-level `var` read back by a provider is the
global lookup this tool exists to remove. `servo.Value[T]()` declares that T comes from the caller:

```go
servo.Build(
    servo.Root[*api.Server](),
    servo.Bind[store.Store, *postgres.DB](),
    servo.Value[conf.Flags](),
)
```

The generated API grows additively. `New` stays, and beside it appear a `Values` struct with a
field per declared type and `NewWith(ctx, Values)`, which `New` calls with the zero value. A
supplied value is matched by type like any constructor parameter and beats any provider that also
produces that type, which is what declaring it means. It has no provider and no lifecycle, and it
is handed in once per app, never per call. A nil pointer, interface, map, func or chan is refused
by `NewWith` before anything is built, and a declared value nothing depends on is a generate-time
diagnostic. Full contract: [`Value`](https://okian.github.io/servo/reference/spec.html#value).

## Configuration from env and files

Most components need a handful of settings. Mark the struct, tag the fields, and take it as a
constructor parameter:

```go
//servo:config prefix=POSTGRES
type dbConfig struct {
    dsn      string        `config:"dsn,required"`         // POSTGRES_DSN
    maxConns int32         `config:"max_conns,default=10"` // POSTGRES_MAX_CONNS
    timeout  time.Duration `config:"timeout,default=30s"`  // POSTGRES_TIMEOUT
    password string        `config:"password,required,secret"`
}

func New(cfg dbConfig) (*Store, error) { ... }
```

`servo generate` writes `servo_config_gen.go` beside the type: plain, steppable parsing that
applies defaults, reads the environment, and reports every missing required variable in one
startup error. The loader lives in the config's own package, so the type and its fields stay
unexported, and the tags are validated at generate time, so a misspelled option or a bad `default=`
fails `servo generate` rather than a deploy. Fields tagged `secret` are never echoed by a generated
error. The tag grammar is deliberately four words, name, `required`, `default=` and `secret`;
anything smarter belongs in the constructor that receives the struct.

Declare `servo.ConfigFile("config.yaml")` in the spec and the same tags also read a file section
(`postgres.max_conns`), with precedence default, then file, then environment, so an operator can
override any file value without editing it. The path's extension (`.json`, `.yaml`/`.yml`, `.toml`)
decides which decoder the generated code carries, so an env-only or JSON app stays stdlib-pure. And
since the generator knows every setting the binary reads, `servo config` prints the operator's
manual without running anything. Full contract:
[Generated configuration](https://okian.github.io/servo/reference/config.html).

## Wiring shared between several injectors

A monorepo's `cmd/api`, `cmd/worker` and `cmd/migrator` each declare their own graph, and below the
transport they are usually the same graph. `servo.Include(fn)` splices another function's marker
list into a `Build` call, so the shared part is written once:

```go
// internal/wiring/wiring.go
//go:build servoinject

package wiring

func Shared() []servo.Marker {
    return []servo.Marker{
        servo.Bind[repository.OrderRepository, *postgres.Store](),
        servo.Scoped[*session.Session, session.Sessions](servo.Linger(5 * time.Minute)),
        // ...
    }
}
```

```go
// cmd/api/spec.go
servo.Build(
    servo.Include(wiring.Shared),
    servo.Root[*api.Server](),
)
```

The function is read, never called, like the spec file itself. So its body has to be exactly
`return []servo.Marker{...}`, and its file needs the `servoinject` tag for the same reason a spec
file does. Markers arrive where the `Include` sits, so a declaration written after it in the spec
overrides the shared one: the local file has the last word. An included set may itself `Include`,
and a cycle is a diagnostic naming the path, not a hang.
[`examples/tutorial`](examples/tutorial) wires four binaries through one
[`internal/wiring/wiring.go`](examples/tutorial/internal/wiring/wiring.go). Full contract:
[`Include`](https://okian.github.io/servo/reference/spec.html#include).

## Capabilities

A component implementing none of these is just constructed and held, and no lifecycle code is
emitted for it. Detection is structural (`types.Implements`) and happens entirely at generation
time, so components never import `servo`:

```go
type Initializer interface{ Init(ctx context.Context) error }
type Runner      interface{ Run(ctx context.Context) error }
type Drainer     interface{ Drain(ctx context.Context) error }
type Flusher     interface{ Flush(ctx context.Context) error }
type Finalizer   interface{ Stop(ctx context.Context) error }
type Healther    interface{ Health(ctx context.Context) error }
type Readier     interface{ Ready(ctx context.Context) error }
```

`Init` runs in topological order, level by level, the nodes of one level concurrently; each call's
context lasts for that call only, so long-lived work belongs in `Run`. A failing or panicking
`Init` cancels its siblings and rolls back what was built. `Run` starts every `Runner` and stops
them all when one fails or panics; once its context is cancelled it waits at most
`servo.DefaultRunGrace` for them. `Shutdown` runs in reverse order, Drain, then Flush, then Stop
plus any cleanup func the constructor returned, each phase budgeted and reported as stopped,
failed or abandoned, never claiming a clean stop it didn't earn. Full contract:
[Lifecycle](https://okian.github.io/servo/reference/lifecycle.html).

## Scoped instances

Everything above is a singleton: constructed once in `New`, held for the life of the process. A
**scope** is the one exception, a keyed sub-graph: one chat room per room name, one workspace per
tenant, one client pool per region. Everyone presenting the same key shares an instance, and the
instance is drained, stopped and evicted once the last holder lets go.

Declare how to extract the key, on the type itself, and an accessor interface in your own package
for consumers to depend on:

```go
// chat/chat.go
type RoomKey string

// The receiver must be unnamed: servo calls this on a typed nil, because it
// needs the key before it can choose an instance.
func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) {
    k, ok := ctx.Value(roomCtxKey{}).(RoomKey)
    if !ok {
        return "", servo.ErrNoScopeKey
    }
    return k, nil
}

// servo can't emit a type into your package, so this is what consumers depend on.
type Rooms interface {
    Acquire(ctx context.Context) (*Room, func(), error)
}
```

Declare the scope in the spec, alongside the roots, then acquire per call:

```go
servo.Scoped[*chat.Room, chat.Rooms](servo.Linger(30*time.Second), servo.Max(10_000)),
```

```go
func NewServer(rooms chat.Rooms, log *logger.Logger) *Server

func (s *Server) Post(ctx context.Context, msg string) error {
    room, release, err := s.rooms.Acquire(ctx)
    if err != nil {
        return err
    }
    defer release()

    room.Post(msg)
    return nil
}
```

The map, the reference count, the linger timer and the per-instance `Init`/`Run`/`Drain`/`Flush`/
`Stop` are all generated. Anything the room transitively depends on that also depends on the key
joins the same scope; anything that doesn't, a logger or a config, stays one shared instance. And
the check a registry beside servo can't give you is a diagnostic: a singleton that takes `*chat.Room`
directly would capture whichever room was built first and hand it to every caller afterwards, so
`servo generate` refuses it and names the two ways out (depend on `chat.Rooms` and acquire per
request, or make the consumer scoped too).

[`examples/scoped`](./examples/scoped) is the runnable version, with a transitively scoped node, a
shared singleton, and the race suite that gates the feature in CI.
[Scoped instances](https://okian.github.io/servo/reference/scopes.html) is the full contract: the
extractor's rules, what belongs to a scope, the linger window, the `Max` cap, the teardown order,
and the four diagnostics.

## HTTP routes

Servo can generate the HTTP layer too: the router, the typed request decoding, the response
encoding and the server lifecycle, all emitted into `servo_gen.go`. A route is a directive comment
on an ordinary exported function: context first, an optional request struct second, then
graph-resolved dependencies:

```go
type OrderReq struct {
    Category string `path:"category"`
    Priority int    `query:"priority"`
    Item     string `json:"item"`
}

//servo:post /order/{category}/
func Order(ctx context.Context, req *OrderReq, st *store.Store) (servo.Json[*OrderResp], error) {
    cat, err := st.Category(req.Category)
    if errors.Is(err, store.ErrNotFound) {
        return nil, servo.Status.NOT_FOUND.Wrapf("category %q: %w", req.Category, err)
    }
    if !cat.Open {
        return nil, servo.Status.FORBIDDEN.New("category is closed")
    }
    ...
    return servo.JSON(resp), servo.Status.CREATED
}
```

The spec opts in with `servo.HTTP(...)`, and the listen addresses, optional TLS files and body
limits come from a `*servo.HTTPConfig` provider you write yourself. The rest, briefly:

- **Patterns** are Go 1.22+ `http.ServeMux` patterns, verbatim. A conflict fails `servo generate`
  with net/http's own message instead of panicking at boot, and a typo'd `//servo:` comment is a
  generate-time error, never a silently unserved route.
- **Binding**: `path`, `query`, `header` and `form` tags bind scalars with generated `strconv`
  parsing, and malformed input is a 400 naming the source. The remaining fields decode from the
  JSON body.
- **Status**: a 2xx `servo.Status` in the error position picks the success code. 4xx messages go
  to the client; 5xx bodies carry canonical text only, while the wrapped detail goes to the log.
- **Listener groups**, one port each: a trailing token (`//servo:get /healthz telemetry`) puts a
  route on its own listener, declared per injector, so a worker binary and an API binary serve
  different subsets. A token no spec declares is a generate-time error.
- **Middleware** is an ordinary graph node with a `Middleware(next http.Handler) http.Handler`
  method, attached in the spec at server, group or route level
  (`servo.Use[*mw.Auth](servo.Group("internal"))`) and wrapping in declaration order, outermost
  first. The commodity kinds ship in
  [`middleware`](https://okian.github.io/servo/reference/middleware.html): Recover, CORS,
  RateLimit, BodyLimit, AccessLog, Timeout, RequestID and Gzip.
- **Extractors** (`servo.Extract[*mw.UserExtractor]()`, with an
  `Extract(r *http.Request) (*auth.User, error)` method) turn every handler parameter of that
  result type into a per-request value. A missing credential becomes a 401 before the handler
  runs.
- **Responses** are a sealed family: `servo.JSON`, `servo.XML`, `servo.Text`, `servo.HTML`,
  `servo.Blob`, `servo.Stream` and `servo.Redirect(url)` with `servo.Status.FOUND`. `nil, nil` is
  an empty 200 and `nil, servo.Status.NO_CONTENT` a 204.

[`examples/http`](./examples/http) is the runnable version, and
[HTTP routes](https://okian.github.io/servo/reference/http.html) the full contract: signature
rules, the binding table, status semantics, and lifecycle.

## Tracing dependencies

A request span from a tracing middleware says a request was slow. It doesn't say which of the
four things the handler called was slow unless each of them is instrumented, and a hand-written
decorator per interface is complete the day it is written and silently one method short after the
next refactor. `servo.Trace[I]()` generates that decorator from the interface's current method set,
every time:

```go
servo.Build(
    servo.Root[*api.Server](),
    servo.Trace[repository.OrderRepository](servo.Expected(domain.ErrNotFound)),
    servo.Trace[cache.OrderCache](servo.Expected(cache.ErrMiss)),
    servo.Trace[broker.EventPublisher](),
)
```

Every consumer of a traced interface receives the wrapper, whatever the interface resolved to: a
`Bind`, a structural match, a `Value`, or in `NewTestApp` the `Override`. Each method with a
leading `context.Context` runs inside a span named `cache.OrderCache/Get`, the implementation
receives the span's context so its own spans nest beneath, and a non-nil error marks the span,
except the ones `servo.Expected` names, because a miss or a not-found is a result, not a failure.
Methods without a context are delegated untouched, and the wrapper has no lifecycle of its own.

The wrapper takes `trace.TracerProvider` as an ordinary graph dependency, a constructor of yours
with no global fallback, and the generated code imports OpenTelemetry in your module; servo's own
module never does. Interfaces only: a consumer holding a concrete type has no seam to wrap, and the
diagnostic says which parameter to change. Full contract:
[Traced interfaces](https://okian.github.io/servo/reference/tracing.html); the tutorial's
[observability chapter](docs/tutorial/16-observability.md) wires it into a real service.

## Multiple instances of the same type

Identity in the graph is purely by type. There is no tagging or naming mechanism, so two
constructors returning the same concrete type aren't two instances, they're an ambiguity:

```
$ servo generate
processor/naive.go:10:6: servo: ambiguous provider for *example.com/dup/sqsaccounts.Client
  ...
  2 functions produce *sqsaccounts.Client, and servo tells values apart by type alone, so they
  are one contested node, not 2 instances:
      processor.NewClientA  processor/naive.go:5:6
      processor.NewClientB  processor/naive.go:6:6
```

The fix, for two SQS clients on two AWS accounts, a primary and a replica database, two tenant
databases, anything of this shape, is to make the two instances distinct *types*, not just distinct
values of one type:

```go
// queue/queue.go
type Client struct{ Account string } // shared underlying client

// Distinct types make these two separate, unambiguous graph nodes.
type OrdersAccount struct{ *Client }
type AuditAccount struct{ *Client }

func NewOrdersAccount() *OrdersAccount { return &OrdersAccount{Client: &Client{Account: "111111111111"}} }
func NewAuditAccount() *AuditAccount   { return &AuditAccount{Client: &Client{Account: "222222222222"}} }
```

```go
// relay/relay.go: needs both accounts at once, so it takes both.
func New(orders *queue.OrdersAccount, audit *queue.AuditAccount) *Relay {
    return &Relay{orders: orders, audit: audit}
}
```

Embedding keeps every `*Client` method directly available, with no delegation boilerplate.
[`examples/basic/queue`](./examples/basic/queue) and [`examples/basic/relay`](./examples/basic/relay)
are the generated, tested version. If nothing ever needs just one of the two on its own, a single
component holding both as plain fields (`type Clients struct{ Orders, Audit *queue.Client }`) is
simpler than two graph nodes.

## Diagnostics

Missing providers, ambiguous bindings and cycles are build failures with source positions, never a
runtime panic, never a guess:

```
$ servo generate
api/api.go:15:6: servo: ambiguous provider for example.com/servobasic/store.Store
  needed by *example.com/servobasic/api.Server  api/api.go:15:6
  root                                          cmd/basic/spec.go:17:3

  3 types in this module implement store.Store — pick one with servo.Bind:
      servo.Bind[store.Store, *memory.Store]()     memory/memory.go:18:6
      servo.Bind[store.Store, *mockstore.Store]()  mockstore/mockstore.go:29:6
      servo.Bind[store.Store, *postgres.DB]()      postgres/postgres.go:13:6
  with these imports in the spec file:
      "example.com/servobasic/memory"
```

Reproduce it by deleting the `servo.Bind[store.Store, *postgres.DB]()` line from
[`examples/basic/cmd/basic/spec.go`](./examples/basic/cmd/basic/spec.go) and running
`servo generate`. [`examples/diagnostics`](./examples/diagnostics) holds eight small, permanently
broken fixtures, each unresolvable in exactly one way and each runnable on its own with
`go run ./cmd/servo generate --dir examples/diagnostics/<name>`.
[Diagnostics](https://okian.github.io/servo/reference/diagnostics.html) explains every message and
its fix.

## CLI

| Command | Purpose |
| --- | --- |
| `servo generate [--dir] [-v]` | Resolve every injector found under `--dir`, write its `servo_gen.go` (and `servo_gen_test.go` where the spec declares `servo.Override`), then type-check what it wrote. The default command. |
| `servo check [--dir]` | Verify every generated file matches a fresh generation and compiles; print a diff for each stale one. Writes nothing. |
| `servo doctor [--dir]` | Diagnose setup problems before `go generate` ever runs: a missing build tag, a stale or absent generated file, a `go:generate` line that can never run, a missing `tool` directive, a servo version other than the one `go.mod` requires, a route naming an undeclared group, and more. |
| `servo init [--dir] [--tags]` | Scaffold the spec file, its `go:generate` file, and, for a `package main` with no `main` yet, a `main.go` calling `servo.Main(New)`. |
| `servo graph [--dir] [--format=text\|json\|dot\|mermaid]` | Export one injector's resolved graph. |
| `servo explain [--dir] [--json] <type>` | Which provider was selected for a type or an interface and why, with its dependencies, dependents, level and capabilities. A type that isn't in the graph gets the reason why not. |
| `servo why [--dir] [--json] [--all] <type>` | The shortest path from a root to that node, naming each interface a step went through; `--all` lists every path. |
| `servo list [--dir] [--json] [--rejected] [--all]` | The candidate index, or every excluded function and the rule that excluded it, plus the routes the module declares. |
| `servo config [--dir] [--json]` | Every setting one injector's `//servo:config` types read: env name, file key, type, default, secret. |
| `servo migrate [--dir]` | Read v1 `Register(X{}, N)` calls and emit a v3 skeleton; see [`examples/migrate`](./examples/migrate). |
| `servo new component <Name>`, `servo new adapter <pkg>`, `servo new mock-adapter <moq\|mockery\|gomock> <Type>` | Print a complete file: a component, a third-party wrapper whose settings are a `//servo:config` type, or the adapter a generated mock needs (see [Mocking](#mocking)). |
| `servo completion <bash\|zsh\|fish>` | A shell completion script, read off the same command table the parser uses. |
| `servo version`, `servo help [command]` | The version (also `--version`); the command list, or one command's usage (also `servo <command> -h`). |

`--dir` (default `.`) is where the module scan starts. `generate`, `check` and `doctor` process
every injector they find within it, so a monorepo's `cmd/api`, `cmd/worker` and `cmd/migrator` are
generated, checked and diagnosed in one pass, and a CI job needs no update when a service is added.
The commands that answer a question about one graph ask you to narrow `--dir` when more than one
injector is in scope. A positional argument is ignored, out loud: `servo generate ./cmd/app` still
generates everything under `--dir` and says so on stderr.

Every command that loads packages also takes `--tags`, `--mod`, `--modfile` and `--overlay`,
spelled exactly as `go build` spells them. `--tags` resolves the graph under those tags and writes
a correspondingly constrained file, so one injector can hold a default and a `--tags=prod` variant
side by side; [`examples/variants`](./examples/variants) does, and
[Build variants](./docs/reference/cli.md#build-variants) is the contract.

`servo-vet` is a `go/analysis` analyzer for the mistakes the compiler can't catch: a marker call in
a file missing the `servoinject` build tag, a `ScopeKey` method whose body can reach its receiver,
a broken `//servo:` directive (a typo'd `//servo:confg` is otherwise just a comment that compiles
and silently loads nothing), and a method with a capability's name but not its signature. All are
caught in the editor. `servovet.Analyzer` is importable by golangci-lint's module plugin system, a
multichecker or `analysistest`; `cmd/servo-vet` is the `singlechecker` binary around it.

**CI and pre-commit**: run `go tool servo check` in CI, so a constructor signature change without
a matching `servo generate` fails the build instead of shipping a stale file.
[servo in CI](docs/servo-in-ci.md) has a workflow to copy, exit codes, workspaces, vendoring,
cross-compiling and measured costs. [`githooks/pre-commit`](./githooks/pre-commit) runs the check
against what is staged; enable it in a clone with `git config core.hooksPath githooks`.

Full reference, every flag included: [CLI commands](https://okian.github.io/servo/reference/cli.html).

## Testing (`servotest`)

```go
func TestApp(t *testing.T) {
    defer servotest.NoLeaks(t) // goleak: clean by construction, or the test says so

    ctx := context.Background()
    app, err := NewTestApp(ctx) // generated only when the spec declares servo.Override
    if err != nil {
        t.Fatal(err)
    }
    ...
    rec := servotest.NewRecorder(app.Report(), app.Shutdown(ctx))
    servotest.AssertStopOrder(t, rec, // asserted, not assumed
        servotest.NodeName[*api.Server](),
        servotest.NodeName[*postgres.DB](),
    )
}
```

- Reports name nodes by type with the full import path, `*example.com/app/api.Server`, never the
  `*api.Server` source code spells. `servotest.NodeName[T]()` produces that name; a hand-written
  short name never matches, and fails an order that was right.
- `servotest.Timeout(t, d)` shrinks `servo.DefaultStopBudget` for one test, so the abandoned-node
  path, a component that ignores cancellation and must be *reported* abandoned rather than claimed
  clean, costs milliseconds instead of five seconds. The budget is a package variable, so `Timeout`
  and `Linger`, its scope-window twin, call `t.Setenv`, and Go refuses to combine either with
  `t.Parallel`.
- A test that parks a goroutine on purpose, to exercise that path, cannot use `NoLeaks`, and
  goleak would report the parked goroutine against whichever test calls `NoLeaks` next. Once a
  package has such a test, every other test in it wants `defer servotest.NoNewLeaks(t)()`: a
  baseline taken now, checked on return. The trailing `()` is the check; without it the line
  compiles and verifies nothing. Prefer `NoLeaks` where nothing parks a goroutine deliberately,
  since it also catches a leak inherited from a sibling test.

Full reference: [servotest package](https://okian.github.io/servo/reference/servotest-package.html).

## Mocking

Servo does not ship a mock generator. Generated code is plain Go, so any type satisfying an
interface can be wired in with `servo.Override`, which swaps the implementation for the whole test
graph that `NewTestApp` builds. The one thing every mocking library needs from you is **a
discoverable, dependency-only constructor**: `servo generate` resolves the graph by calling a
provider function it found, `func F(deps...) T`, never by evaluating a literal written in a test
file. Hand-written fakes already look like that
([`examples/basic/mockstore`](./examples/basic/mockstore)). Generated mocks often don't, because
their constructors take a `*testing.T` or a `*gomock.Controller`, a per-test value the graph has no
way to supply. The fix in every case is a few lines in a separate, hand-written file beside the
generated mock (never edit a `// Code generated ... DO NOT EDIT` file; regenerating the mock would
erase it). `servo new mock-adapter <moq|mockery|gomock> <GeneratedTypeName>` prints that file, and
[`examples/mocking`](./examples/mocking) wires all three tools into real, `go test`-checked apps,
each with the generated mock committed.

**A test can also hand the mock in.** `NewTestApp` builds each Override through its constructor,
but `NewTestAppWith` takes a `TestValues` with a field per Override, typed as the interface. Leave
it nil and the adapter builds the mock as below; set it and your instance is used instead, built in
the test with the test's own `t`, so expectations report through `t` and tests run in parallel:

```go
mock := mockgenstore.NewMockStore(gomock.NewController(t)) // reports through t, finishes on t.Cleanup
mock.EXPECT().Get("user:42").Return("mocked-value")

app, err := NewTestAppWith(ctx, TestValues{Store: mock})
```

The adapter constructor is still what `NewTestApp` uses; it just stops being the only way in. The
rules for which Overrides get a field are in
[Generated API](docs/reference/generated-api.md#supplying-an-override-per-call); mocks always
qualify.

### `moq`

`moq` generates a plain struct of function fields and no constructor at all. Add one in a separate
file, and override with the mock's type:

```go
// mockstore/adapter.go
func NewStoreMock() *StoreMock { return &StoreMock{} }
```

```go
servo.Override[store.Store, *mockstore.StoreMock](),
```

```go
app, _ := NewTestApp(ctx)
app.storeMock.GetFunc = func(key string) string { return "mocked:" + key }
got := app.server.Lookup("user:42")   // "mocked:user:42"
app.storeMock.GetCalls()              // [{Key: "user:42"}]
```

Runnable version: [`examples/mocking/moq`](./examples/mocking/moq).

### `testify` + `mockery`

Mockery's generated `NewStore(t)` takes a `*testing.T`-shaped value so it can register
`AssertExpectations` on cleanup. That is a valid provider shape, so a second, zero-arg constructor
returning the same `*Store` would be a genuine ambiguity. Wrap instead of competing:

```go
// mocks/adapter.go
type StoreMock struct{ *Store } // a distinct result type, so no collision

func NewStoreMock() *StoreMock { return &StoreMock{Store: &Store{}} }
```

```go
servo.Override[store.Store, *mocks.StoreMock](),
```

```go
app, _ := NewTestApp(ctx)
app.storeMock.On("Get", "user:42").Return("mocked-value")

got := app.server.Lookup("user:42") // "mocked-value"

app.storeMock.AssertExpectations(t) // call this yourself; nothing auto-registered it
```

This is the cleanest of the three: testify takes `t` at the point you call `AssertExpectations`,
where a real `t` is in scope, and a failed expectation is a clean, isolated test failure. Runnable
version: [`examples/mocking/mockery`](./examples/mocking/mockery).

### `go.uber.org/mock` (gomock)

Same wrapping, for the same reason: `NewMockStore(ctrl)` is a valid, colliding provider shape. The
harder part is the `*gomock.Controller`, which needs a `TestReporter` from somewhere.
`servotest.PanicReporter` is one, satisfying gomock's interface structurally without adding a
gomock dependency to `servotest`:

```go
// mockgenstore/adapter.go
type MockStoreForServo struct {
    *MockStore
    Finish func()
}

func NewMockStoreForServo() *MockStoreForServo {
    ctrl := gomock.NewController(servotest.PanicReporter{})
    return &MockStoreForServo{MockStore: NewMockStore(ctrl), Finish: ctrl.Finish}
}
```

```go
app, _ := NewTestApp(ctx)
defer app.mockStoreForServo.Finish() // a plain defer in the test, right after construction
app.mockStoreForServo.EXPECT().Get("user:42").Return("mocked-value")

got := app.server.Lookup("user:42") // "mocked-value"
```

The trade-off is that with no `t` to report to, an unmet expectation panics. Called from the test's
own goroutine, Go's test runner names the failing test before the process exits. Routed through
servo's `(T, func())` cleanup shape instead, `Finish` would run inside the goroutine `Shutdown`
gives each stop, where the panic is recovered into the report as a failed node, and a test that
never inspects `app.Shutdown(ctx)` passes with the expectation unmet. So keep it in the `defer`.

That is the adapter's limit, not the graph's. For gomock's strict verification, hand the test's own
mock to `NewTestAppWith` as above: the controller holds the real `t`, an unmet expectation is an
ordinary test failure, and `Finish` runs on `t.Cleanup` without anyone remembering it.
[`examples/mocking/gomock`](./examples/mocking/gomock) is the runnable version, and its
[`percall_test.go`](./examples/mocking/gomock/cmd/app/percall_test.go) does exactly this.

## Layout

```
cmd/servo/            CLI: generate, check, graph, explain, why, list, config, init, doctor,
                      migrate, new, completion, version, help
cmd/servo-vet/        singlechecker binary wrapping servovet.Analyzer
internal/load/        go/packages → typed syntax, spec-file discovery, Include splicing
internal/graph/       Key, Provider, candidate index, capability detection
internal/route/       //servo: HTTP directive scan and validation
internal/resolve/     roots → closure → order, levels, diagnostics
internal/emit/        source emission, import manager, name allocator
internal/render/      text, JSON, DOT, Mermaid graph renderers
servo/                markers + the public runtime: Main, reports, budgets, HTTP types, responses
servoimpl/            the runtime generated code calls into; not for direct use
middleware/           shipped HTTP middleware: Recover, CORS, RateLimit, BodyLimit, …
conf/                 stdlib-only coercions behind generated config-file reading
servotest/            NoLeaks, NoNewLeaks, Recorder, AssertInitOrder, AssertStopOrder, Timeout,
                      Linger, PanicReporter
servovet/             the go/analysis Analyzer, importable by golangci-lint and analysistest
examples/basic/       a complete, runnable example (separate module)
examples/scoped/      keyed, refcounted instances + the race suite (separate module)
examples/http/        //servo: routes end to end against a real listener (separate module)
examples/mocking/     moq/mockery/gomock integrations, one binary each (separate module)
examples/variants/    one injector, two graphs, selected by a build tag (separate module)
examples/diagnostics/ eight permanently-broken fixtures, one per failure mode (separate module)
examples/tutorial/    the layered microservice docs/tutorial/ builds (separate module)
examples/migrate/     a pre-servo codebase for `servo migrate` to read (part of this module)
```

Core (`internal/*`, `cmd/servo`, `servovet`) depends on nothing beyond `golang.org/x/tools`;
`servotest` alone depends on `go.uber.org/goleak`. Neither the runtime packages nor any generated
output import `reflect` for servo's own wiring (`encoding/json`, used by emitted HTTP servers,
reflects internally like it does for everyone). Generated code imports nothing from servo's module
but its runtime: `servo`, `servoimpl`, and `conf` for config loaders. `TestConformance` in
`internal/emit` enforces both promises, over every golden file and the runtime packages.

## Contributing

Bug reports and feature requests go through [GitHub Issues](https://github.com/okian/servo/issues);
that's also the public archive of past reports and their resolutions. See
[CONTRIBUTING.md](CONTRIBUTING.md) for the dev workflow and what a PR is expected to include, and
[SECURITY.md](SECURITY.md) to report a vulnerability privately.
