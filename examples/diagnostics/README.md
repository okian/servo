# Diagnostics

Eight fixtures, each deliberately unresolvable in exactly one way, showing what `servo generate`
reports for each. None of these packages have a `main.go` or a committed `servo_gen.go` — they
can't, since generation always fails for them. `go build ./...` still succeeds: each `spec.go` is
gated by the `servoinject` build tag, so the normal build only sees the plain, valid Go around it.

Run any of these yourself from the repo root:

```
go run ./cmd/servo generate --dir examples/diagnostics/missing
```

## `missing/`

`Server` depends on the `Store` interface; nothing in scope implements it, so there's no
candidate list to offer:

```
servo: no provider for example.com/servodiagnostics/missing.Store
  needed by *example.com/servodiagnostics/missing.Server  missing/server.go:11:6
  root                                                     missing/spec.go:9:3
```

## `ambiguous/`

`Server` depends on the same `Store` interface, but now two types (`Postgres`, `Redis`) both
implement it and nothing picks one with `servo.Bind`:

```
servo: no provider for example.com/servodiagnostics/ambiguous.Store
  needed by *example.com/servodiagnostics/ambiguous.Server  ambiguous/store.go:23:6
  root                                                       ambiguous/spec.go:9:3

  2 types implement example.com/servodiagnostics/ambiguous.Store — add one of:
      servo.Bind[example.com/servodiagnostics/ambiguous.Store, *example.com/servodiagnostics/ambiguous.Postgres]()      ambiguous/store.go:11:6
      servo.Bind[example.com/servodiagnostics/ambiguous.Store, *example.com/servodiagnostics/ambiguous.Redis]()      ambiguous/store.go:17:6
```

## `cycle/`

`A` depends on `*B` and `B` depends back on `*A`:

```
servo: dependency cycle detected
  *example.com/servodiagnostics/cycle.A  cycle/graph.go:8:6
  *example.com/servodiagnostics/cycle.B  cycle/graph.go:12:6
  *example.com/servodiagnostics/cycle.A  cycle/graph.go:8:6  (cycle closes here, back to the first line)
```

## `widening/`

The scope diagnostic this feature exists for. `Server` is a singleton that takes `*Room` — a scoped
type — directly instead of the `Rooms` accessor interface. Built once and held for the life of the
process, it would capture whichever room happened to be built first and hand that same one to every
caller afterwards:

```
servo: *example.com/servodiagnostics/widening.Room is scoped, but *example.com/servodiagnostics/widening.Server is a singleton that depends on it
  needed by *example.com/servodiagnostics/widening.Server  widening/rooms.go:37:6
  root                                                     widening/spec.go:9:3

  ...

  Two ways out:
    - depend on the accessor instead: change widening.NewServer's parameter from *…widening.Room to …widening.Rooms,
      and call Acquire(ctx) per request
    - make *…widening.Server scoped too, by giving it a dependency on …widening.RoomKey
```

## `crossscope/`

`Room` is keyed by `RoomKey` but takes a `*Tenant`, which is keyed by `TenantKey`. That is a nested
scope, rejected on purpose:

```
servo: *…crossscope.Room and *…crossscope.Tenant are in different scopes
  ...
  Nested scopes are deliberately not supported in this release: one instance
  per key pair means two reference counts and two linger windows with no single
  owner ...
```

## `extractor/`

`Session`'s `ScopeKey` method takes a `*Decoder`, which is itself scoped. The extractor is what
decides which instance a caller gets, so it runs before any instance exists:

```
servo: *…extractor.Session's ScopeKey extractor depends on *…extractor.Decoder, which is itself scoped
  ScopeKey                      extractor/session.go:36:17
  needed by *…extractor.Decoder extractor/session.go:25:6
  needed by *…extractor.Session extractor/session.go:32:6
  root                          extractor/spec.go:10:3
  ...
```

## `undeclared/`

`Tenant` has a `ScopeKey` method but no `servo.Scoped` names it. servo will not infer the accessor
interface, because it cannot emit a type into your package — so it prints the declaration to add.
(If the package already declares an interface the generated accessor would satisfy, the message
names that one instead and asks only for the marker.)

```
servo: *…undeclared.Tenant declares a ScopeKey method but no servo.Scoped declares it
  ...
  In package undeclared:

	type Tenants interface {
	    Acquire(ctx context.Context) (*Tenant, func(), error)
	}
```

## `noscopekey/`

The reverse of `undeclared/`, and the half a new user meets first: `servo.Scoped[*Cache, Caches]`
declares the scope, the accessor interface is written correctly, and `Cache` has no `ScopeKey`
method — so nothing can decide which instance a caller gets:

```
servo: servo.Scoped[*…noscopekey.Cache, …noscopekey.Caches] declares a scope, but
*…noscopekey.Cache has no ScopeKey method

  Add one. The receiver must be unnamed — generated code calls it on a typed nil,
  because it needs the key before it can choose an instance:

	func (*Cache) ScopeKey(ctx context.Context) (RoomKey, error) {
	    ...
	}
```
