# Migrate

`legacy.go` stands in for a pre-servo codebase: components register themselves with a global
sequencer and a hand-maintained order (`Register(component, order)`), the pattern `servo migrate`
reads to produce a starting spec file. It has no dependency on servo at all and needs none —
`servo migrate` only parses syntax, it never type-checks or loads packages, so there's nothing to
build against.

`Cache` and `DB` deliberately share order 2 — a duplicate the report below is meant to catch, not
a mistake in this fixture.

Run it yourself from the repo root:

```
go run ./cmd/servo migrate --dir examples/migrate
```

```
servo migrate report:
  v1 has no constructor parameters, so there is no real dependency graph to derive
  an order from — this only surfaces the OLD order values for review.

  order=1    Logger                         examples/migrate/legacy.go:21:2
  order=2    DB                             examples/migrate/legacy.go:22:2  <- shares this order with another service: a likely latent ordering bug
  order=2    Cache                          examples/migrate/legacy.go:23:2  <- shares this order with another service: a likely latent ordering bug
  order=3    Server                         examples/migrate/legacy.go:24:2

Skeleton spec (add real constructor dependencies by hand — v1's global-lookup
style can't be inferred automatically):

//go:build servoinject

package main

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Logger](), // was order=1
		servo.Root[*DB](), // was order=2
		servo.Root[*Cache](), // was order=2
		servo.Root[*Server](), // was order=3
	)
}
```

The skeleton is a starting point, not a finished spec: `Register`'s global-lookup style means
`servo migrate` has no way to know that, say, `Server` actually needs `*DB` — it only knows the
type existed and what order it used to run in. Turning this into a real spec means replacing each
bare `Root` with the actual constructor dependencies by hand, the same way you'd write one from
scratch (see the [Quick start](../../README.md#quick-start)).
