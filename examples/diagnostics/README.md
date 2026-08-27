# Diagnostics

Three fixtures, each deliberately unresolvable in exactly one way, showing what `servo generate`
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
