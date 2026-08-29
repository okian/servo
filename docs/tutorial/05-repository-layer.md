# 5. Repository layer

Now that `domain` exists, we need somewhere to actually put an order. This chapter builds that in
two pieces: an interface describing *what* the service layer needs from storage, written before
any storage technology is chosen, and a real Postgres implementation of it. By the end, you'll have
a working, tested repository — and a real database running in Docker to prove it against.

## Declare what you need before deciding how to store it

Create `repository/repository.go`:

```go
package repository

import (
	"context"
	"uuid"

	"example.com/servoorders/domain"
)

type OrderRepository interface {
	Create(ctx context.Context, o *domain.Order) error
	Get(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Order, error)
}

type UserRepository interface {
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
}
```

Writing the interface first, before `postgres` exists at all, is the same principle from
[chapter 4](04-domain-layer.md#add-the-errors-every-other-layer-will-check-for) applied one layer
up: whoever *consumes* an interface should own it. `postgres` is about to import `repository`;
`repository` will never import `postgres`. Keep that direction in mind as you write the
implementation below — it's what will let [chapter 15](15-testing-strategy.md) swap in a mock
without either side knowing it happened.

## Build the Postgres implementation

Create `postgres/postgres.go`. Start with the type and its constructor:

```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"example.com/servoorders/config"
	"example.com/servoorders/domain"
	"example.com/servoorders/migrations"
	"example.com/servoorders/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

var (
	_ repository.OrderRepository = (*Store)(nil)
	_ repository.UserRepository  = (*Store)(nil)
)

const envPrefix = "POSTGRES_"

type Config struct {
	DSN string `env:"DSN,required"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

func New(cfg *Config) (*Store, error) {
	pool, err := pgxpool.New(context.Background(), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}
```

The two `var _ repository.X = (*Store)(nil)` lines aren't runtime code — they're compile-time
assertions. If `Store` ever stops satisfying either interface, the build fails right here, with a
clear message, instead of failing somewhere far away the first time something tries to use it as
one.

`pgxpool.New` doesn't actually open a connection yet; it only validates the DSN and prepares a pool
lazily. Add the three methods that give this type real startup, shutdown, and health behavior:

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

This is the first point where the real connectivity check happens — a broken DSN or an unreachable
database now fails at startup, not on the first request that happens to touch it. Hold onto the
names `Init`, `Stop`, and `Health` for a moment: [chapter 11](11-wiring-with-servo.md) is where
we'll see servo discover and call these automatically, purely because they exist on the type — not
because `Store` imports servo or registers itself anywhere. It doesn't; check the imports above
again if you like. That's worth sitting with now, before more components pick up the same pattern
and it starts to feel routine.

Now the actual queries. `Get` is the one to look at closely, because it has to make a decision
`Create` and the others don't — what happens when the row simply isn't there:

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

`pgx.ErrNoRows` is pgx's own way of saying "the query succeeded, it just matched nothing." Without
that `errors.Is` check, a perfectly normal "no such order" would come back as an opaque wrapped
error instead of the `domain.ErrNotFound` every layer above this one already knows how to handle —
and three layers up, in [the API layer](10-api-layer.md), that's the difference between a clean 404
and a confusing 500. `GetByUsername` needs the exact same check, for the same reason; write it now
while the pattern is fresh:

```go
func (s *Store) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, password_hash FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get user: %w", err)
	}
	return &u, nil
}
```

`Create` and `ListByUser` round out the interface — no new ideas in either, just the same
query/scan shape:

```go
func (s *Store) Create(ctx context.Context, o *domain.Order) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO orders (id, user_id, item, quantity, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		o.ID, o.UserID, o.Item, o.Quantity, o.Status, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: create order: %w", err)
	}
	return nil
}

func (s *Store) ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]*domain.Order, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, item, quantity, status, created_at FROM orders
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("postgres: list orders: %w", err)
	}
	defer rows.Close()

	var orders []*domain.Order
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.Item, &o.Quantity, &o.Status, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan order: %w", err)
		}
		orders = append(orders, &o)
	}
	return orders, rows.Err()
}
```

Notice `ListByUser` deliberately has no `pgx.ErrNoRows` check — a user with zero orders is a
perfectly normal, empty result, not an error. That distinction (one query where "nothing found"
means "fail," another where it just means "empty") is worth noticing now, because it's easy to
copy-paste the wrong one once there are more query methods than these four.

## Give the database something to migrate to

`Store.Init` calls `migrations.Run`, which doesn't exist yet. Rather than reach for a full
migration library for four tables, we'll write a small runner ourselves — about forty lines,
everything visible in one file, nothing to look up in someone else's documentation to understand
what it does. (A tool like [`golang-migrate`](https://github.com/golang-migrate/migrate) or
[`goose`](https://github.com/pressly/goose) is genuinely the better choice once a team has more
than a handful of migrations, or needs to roll one back in production — see
[chapter 19](19-alternatives-and-further-reading.md#migrations) for when to make that switch.)

Create `migrations/migrations.go`:

```go
package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

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

`//go:embed *.sql` compiles every `.sql` file in this directory straight into the binary — no
separate migrations directory to deploy alongside it, no path to get wrong at runtime. `apply` is
the part that makes running this on every startup safe rather than alarming:

```go
func apply(ctx context.Context, pool *pgxpool.Pool, name string) error {
	var applied bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
	).Scan(&applied)
	if err != nil {
		return fmt.Errorf("migrations: check %s: %w", name, err)
	}
	if applied {
		return nil
	}

	body, err := files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("migrations: read %s: %w", name, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrations: begin %s: %w", name, err)
	}
	defer tx.Rollback(ctx) // no-op once Commit succeeds

	if _, err := tx.Exec(ctx, string(body)); err != nil {
		return fmt.Errorf("migrations: apply %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return fmt.Errorf("migrations: record %s: %w", name, err)
	}
	return tx.Commit(ctx)
}
```

Each file is checked against `schema_migrations`, skipped if it's already there, and — this is the
part that matters — applied and recorded inside one transaction, so a failure partway through a
file never leaves the tracking table lying about what actually happened.

Now write the SQL itself. Create `migrations/0001_create_tables.sql`:

```sql
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

CREATE INDEX orders_user_id_idx ON orders(user_id);
```

And `migrations/0002_seed_users.sql`, so there's something to log in against before
[chapter 9](09-authentication.md) exists — two accounts, both with the password `password123`,
bcrypt-hashed for real rather than a placeholder string:

```sql
INSERT INTO users (id, username, password_hash) VALUES
    ('11111111-1111-1111-1111-111111111111', 'alice', '$2a$10$7zAE.OJ3lyD/3Eq4T2XMV.zU162KDzXOQnpHXE/kkd00GSQ.V4I3.'),
    ('22222222-2222-2222-2222-222222222222', 'bob',   '$2a$10$SHJKrTmOvH8SK6p6gGxu9e3.d7Z3DuDbhHOFQ22G0E5zz0GEj3YM.');
```

A real service would never ship credentials in a migration file — this exists purely because a
signup flow is out of scope for this tutorial, and the login endpoint needs *something* to check
against.

## Run it against a real database

This is the first chapter where there's something worth actually running. Start Postgres:

```
$ make up
```

And run the tests:

```
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

The postgres tests are worth writing yourself, in `postgres/postgres_test.go` — but first, a small
helper that every one of them shares, and that's worth understanding before the tests that call it:

```go
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; see docs/tutorial/05-repository-layer.md")
	}

	s, err := New(&Config{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init (is Postgres running and reachable at %s?): %v", dsn, err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s
}
```

Without `make up` first, the same command doesn't fail — it just quietly finds nothing to test:

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

That skip-with-a-pointer, instead of a failure, is `testStore`'s whole job. And every time it
*does* run, it's also proving the migrations apply cleanly on a fresh database — `Init` runs them
every time, so a passing test already tells you the schema and the seed data are both correct.

## Diagnostics

- **`postgres: ping: failed to connect ... connection refused`** — Postgres isn't running, or isn't
  listening where the DSN says. Confirm `make up` actually succeeded (`docker ps`), and that
  `POSTGRES_DSN`'s host and port match the compose file's.
- **`could not create directory "/var/lib/postgresql/data/pg_wal": No space left on device`** (in
  the container's own logs, not your Go program) — this isn't your host disk; it's Docker Desktop's
  VM disk allocation, usually filled by *other*, unrelated projects' volumes and images. Check
  `docker system df` — if `Local Volumes` shows a large reclaimable amount, something else on your
  machine is the cause. Don't blindly `docker volume prune`; check `docker volume ls` first so you
  don't delete a different project's data.
- **`pgx.ErrNoRows` leaking out as a generic error** — every query that can legitimately return
  zero rows for "not found" (as opposed to "empty list") needs the `errors.Is(err, pgx.ErrNoRows)`
  check from `Get` and `GetByUsername` above. Forgetting it turns a clean 404 into a confusing 500,
  three layers up.
- **Migrations show as "already applied" but the schema looks wrong** — the tracking table only
  records that a file *ran*, not that every session of running it succeeded end to end. If you edit
  an already-applied migration file, `Run` won't re-apply it — add a new file instead. Never edit a
  migration that's already shipped.

## Do's and don'ts

- **Do** run migrations from `Init`, not a separate manual step, for a service this size — one less
  thing to forget before a deploy. A larger service with its own release pipeline often runs
  migrations as a distinct step instead, so schema changes and code deploys can be reasoned about
  independently; that's a real difference in scale, not a correction to what we just built.
- **Do** wrap every persistence error with `fmt.Errorf("postgres: ...: %w", err)` — the prefix
  makes a stack of wrapped errors readable in a single log line, and `%w` keeps
  `errors.Is`/`errors.As` working through it.
- **Don't** let SQL live anywhere but this package. A query written directly in the service layer
  "just this once" is how the interface boundary quietly stops meaning anything.
- **Don't** use `SELECT *`. Naming columns explicitly means adding a column to the table later
  doesn't silently change what `Scan` expects.

## Next

[Chapter 6: Caching layer](06-caching-layer.md) — Redis, and the cache-aside pattern this service
uses for order lookups.
