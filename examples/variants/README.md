# Build variants

One injector, two graphs, selected by a build tag.

```
go run ./cmd/app              # hello from memory
go run -tags=prod ./cmd/app   # hello from postgres
```

## What makes this a variant rather than a runtime switch

`memory.Mem` is behind `//go:build !prod` and `postgres.DB` is behind `//go:build prod`, so in each
configuration only one of the two types *exists*. They also have different lifecycles — the database
has `Init` and `Stop`, the in-memory store has neither — so the two configurations produce genuinely
different construction and shutdown plans, not the same plan with a different value plugged in.

That is the case a single generated file cannot serve, and it is why each configuration needs its
own spec file: a `servo.Bind[store.Store, *postgres.DB]()` naming a type that only exists under
`prod` could not type-check in the default build.

## The two spec files

```go
// cmd/app/spec.go
//go:build servoinject && !prod

// cmd/app/spec_prod.go
//go:build servoinject && prod
```

Servo mirrors each spec's own constraint into the file it generates, negating only `servoinject`:

| Spec file | Generated file | Its constraint |
| --- | --- | --- |
| `spec.go` — `servoinject && !prod` | `servo_gen.go` | `!servoinject && !prod` |
| `spec_prod.go` — `servoinject && prod` | `servo.prod_gen.go` | `!servoinject && prod` |

The `&& !prod` on the default spec is the load-bearing part. Servo never invents a negation, so if
you leave it off, both generated files are satisfied by `go build -tags=prod` and the package
declares `App` and `New` twice — which servo detects and refuses to generate rather than letting you
discover at build time.

## Running it

Everything is done once per configuration:

```
go build ./...                 && go build -tags=prod ./...
go test ./...                  && go test -tags=prod ./...
servo check                    && servo check --tags=prod
```

`servo doctor` reports the variant matching its own flags and lists the others it did not check, so
a second variant is never silently invisible.
