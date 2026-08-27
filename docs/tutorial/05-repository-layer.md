# 5. Repository layer

## The interfaces come first

`repository` declares what the service layer needs, before anything about *how* it's stored
exists:

```go
// examples/tutorial/repository/repository.go
type OrderRepository interface {
	Create(ctx context.Context, o *domain.Order) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Order, error)
}

type UserRepository interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
}
```

This is the same principle from [chapter 4](04-domain-layer.md#why-sentinel-errors-and-why-here)
applied one layer up: the interface is *owned by the consumer* (the service layer, and this
package that sits just below it), not by whatever eventually implements it. `postgres` will import
`repository`; `repository` will never import `postgres`. That's what makes
[`servo.Override`](11-wiring-with-servo.md) able to swap in a mock for tests without either
`service` or `postgres` knowing it happened.

## The Postgres implementation

One `Store` type satisfies both interfaces:

```go
// examples/tutorial/postgres/postgres.go
type Store struct {
	pool *pgxpool.Pool
}

func New(cfg *config.Config) (*Store, error) {
	pool, err := pgxpool.New(context.Background(), cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}
```

`pgxpool.New` doesn't actually connect — it validates the DSN and prepares a connection pool
lazily. The real connectivity check happens in `Init`, which is where a broken DSN or an
unreachable database becomes a startup failure instead of the first request's problem:

```go
func (s *Store) Init(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres: ping: %w", err)
	}
	return migrations.Run(ctx, s.pool)
}

func (s *Store) Stop(context.Context) error {
	s.pool.Close()
	return nil
}

func (s *Store) Health(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
```

`Init`, `Stop`, and `Health` are servo's capability methods — [chapter
11](11-wiring-with-servo.md#capabilities-recap) covers all of them together once every component
has some to compare, but the shape to notice now: **`Store` never imports `servo`**. It implements
these methods because they're useful on their own terms (a real startup check, a real shutdown, a
real health probe); servo detects and calls them structurally, the same way `encoding/json` detects
`MarshalJSON` without an import.

`Get` and `GetByUsername` translate "no such row" into the domain's own not-found error, so nothing
above this package ever needs to know pgx exists:

```go
func (s *Store) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	var o domain.Order
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, item, quantity, status, created_at FROM orders WHERE id = $1`, id,
	).Scan(&o.ID, &o.UserID, &o.Item, &o.Quantity, &o.Status, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get order: %w", err)
	}
	return &o, nil
}
```

## Migrations: a small hand-rolled runner, on purpose

Schema changes live as embedded SQL files, applied once each, tracked in their own table:

```go
// examples/tutorial/migrations/migrations.go
//go:embed *.sql
var files embed.FS

// Run is idempotent: safe to call on every startup, since already-applied
// files are skipped.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrations: create tracking table: %w", err)
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("migrations: glob: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := apply(ctx, pool, name); err != nil {
			return err
		}
	}
	return nil
}
```

`apply` (not shown — see the file directly) does the actual per-file work: skip if
`schema_migrations` already has this name, otherwise read the embedded file, run it and record it
in one transaction so a failure partway through never leaves the tracking table out of sync with
what actually ran.

```sql
-- examples/tutorial/migrations/0001_create_tables.sql
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE orders (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id),
    item       TEXT NOT NULL,
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    status     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

A tool like [`golang-migrate`](https://github.com/golang-migrate/migrate) or
[`goose`](https://github.com/pressly/goose) does more than this — down-migrations, dirty-state
detection, a CLI — and is genuinely the better choice once a team has more than a handful of
migrations or needs to roll one back in production. This tutorial hand-rolls forty lines instead,
deliberately, so there's no library behavior to explain that isn't visible in the file itself; see
[chapter 18](18-alternatives-and-further-reading.md#migrations) for when to switch.

`0002_seed_users.sql` inserts two demo accounts (`alice` and `bob`, both with password
`password123`, bcrypt-hashed for real) purely so [chapter 9](09-authentication.md)'s login endpoint
has something to authenticate against without a signup flow, which stays out of scope.

## Try it yourself

Start Postgres, then run the integration tests against it for real:

```
$ make up
$ make test-integration
=== RUN   TestNewFailsWhenARequiredVarIsMissing
--- PASS: TestNewFailsWhenARequiredVarIsMissing (0.00s)
=== RUN   TestNewAppliesDefaultsAndParsesTypedFields
--- PASS: TestNewAppliesDefaultsAndParsesTypedFields (0.00s)
PASS
ok  	example.com/servoorders/config	0.351s
?   	example.com/servoorders/domain	[no test files]
?   	example.com/servoorders/migrations	[no test files]
=== RUN   TestCreateAndGetOrder
--- PASS: TestCreateAndGetOrder (0.02s)
=== RUN   TestGetMissingOrderReturnsErrNotFound
--- PASS: TestGetMissingOrderReturnsErrNotFound (0.01s)
=== RUN   TestListByUserOrdersMostRecentFirst
--- PASS: TestListByUserOrdersMostRecentFirst (0.01s)
=== RUN   TestGetByUsernameFindsSeededUser
--- PASS: TestGetByUsernameFindsSeededUser (0.01s)
=== RUN   TestGetByUsernameUnknownReturnsErrNotFound
--- PASS: TestGetByUsernameUnknownReturnsErrNotFound (0.01s)
PASS
ok  	example.com/servoorders/postgres	0.376s
?   	example.com/servoorders/repository	[no test files]
```

`test-integration` runs `go test ./... -v`, so config's tests and the "no test files" packages
show up alongside postgres's — that's the whole suite, not just this chapter's slice of it.

Without `make up` first, the same command doesn't fail, it just quietly has nothing to test:

```
$ go test ./postgres/...
ok  	example.com/servoorders/postgres	0.174s
```

Add `-v` to see why it was that fast:

```
$ go test ./postgres/... -v
--- SKIP: TestCreateAndGetOrder (0.00s)
    postgres_test.go:40: TEST_POSTGRES_DSN not set; see docs/tutorial/05-repository-layer.md
...
PASS
```

That's `testStore`'s whole job — check `TEST_POSTGRES_DSN`, skip with a pointer back to this page
if it's unset, otherwise connect and run `Init` for real (which also proves the migrations apply
cleanly on a fresh database, every time the suite runs).

## Diagnostics

- **`postgres: ping: failed to connect ... connection refused`** — Postgres isn't running or isn't
  listening where the DSN says. Check `make up` actually succeeded (`docker ps`), and that
  `POSTGRES_DSN`'s host/port match the compose file's.
- **`could not create directory "/var/lib/postgresql/data/pg_wal": No space left on device`** (in
  the container's logs, not your Go program) — this isn't your host disk; it's Docker Desktop's own
  VM disk allocation, usually filled by *other* projects' unrelated volumes and images. Check
  `docker system df` — if `Local Volumes` shows a large reclaimable amount, something else on your
  machine is the cause. Don't blindly `docker volume prune`; check `docker volume ls` first so you
  don't delete a different project's data.
- **`pgx.ErrNoRows` leaking out of the repository as a generic error** — every query that can
  legitimately return zero rows needs the `errors.Is(err, pgx.ErrNoRows)` check shown above.
  Forgetting it means a 404-shaped situation surfaces as a 500 three layers up, in
  [the API layer](10-api-layer.md).
- **Migrations "already applied" but the schema looks wrong** — the tracking table only records
  that a file *ran*, not that it ran successfully to completion in every session (a transaction
  failure rolls back the SQL but, if the code had a bug, could theoretically still have recorded
  success). If you edit an already-applied migration file, `Run` won't re-apply it — add a new file
  instead. Never edit a migration that's already shipped.

## Do's and don'ts

- **Do** run migrations from application startup code (`Init`), not a separate manual step, for a
  service this size — one less thing to forget before a deploy. A larger service with a dedicated
  release pipeline often runs migrations as their own step instead, so schema changes and code
  deploys can be reasoned about independently; that's a legitimate difference in scale, not a
  correction to this choice.
- **Do** wrap every persistence error with `fmt.Errorf("postgres: ...: %w", err)` — the prefix
  makes a stack of wrapped errors readable in a log line, and `%w` keeps `errors.Is`/`errors.As`
  working through it.
- **Don't** let SQL live anywhere but this package. A query embedded in the service layer "just
  this once" is how the interface boundary quietly stops meaning anything.
- **Don't** use `SELECT *`. Naming columns explicitly means adding a column to the table doesn't
  silently change what `Scan` expects.

## Next

[Chapter 6: Caching layer](06-caching-layer.md) — Redis, and the cache-aside pattern this service
uses for order lookups.
