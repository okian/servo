# 6. Caching layer

Every `GetOrder` call right now would hit Postgres directly. That's fine at low volume, but order
lookups are exactly the kind of read that's cheap to cache and expensive to keep re-querying — so
before writing the service layer that ties everything together, let's give it a cache to read
through. Same pattern as the last chapter: declare what's needed as an interface, then build a real
implementation against Redis.

## Declare the cache boundary

Create `cache/cache.go`:

```go
package cache

import (
	"context"
	"errors"
	"uuid"

	"example.com/servoorders/internal/domain"
)

var ErrMiss = errors.New("cache: miss")

type OrderCache interface {
	Get(ctx context.Context, id uuid.UUID) (*domain.Order, error)
	Set(ctx context.Context, o *domain.Order) error
	Invalidate(ctx context.Context, id uuid.UUID) error
}
```

`ErrMiss` deserves a moment of thought before you move past it. It would be tempting to reuse
`domain.ErrNotFound` here — a cache miss and "this order doesn't exist" can look like the same
situation from the outside. They aren't: `domain.ErrNotFound` means the repository looked and the
row genuinely isn't there; `ErrMiss` just means "the cache doesn't have an answer, ask the
repository instead." Give them different sentinels now, in this package, and the service layer we
write in the next chapter will be able to tell the two apart without any extra work.

## Implement it against Redis

Create `cache/redis/redis.go`. Start with the type, a fixed TTL, and the constructor:

```go
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"

	"example.com/servoorders/internal/cache"
	"example.com/servoorders/internal/domain"
	goredis "github.com/redis/go-redis/v9"
)

const ttl = 5 * time.Minute

type Cache struct {
	client *goredis.Client
}

var _ cache.OrderCache = (*Cache)(nil)

//servo:config prefix=REDIS
type config struct {
	addr string `config:"addr,required"`
}

func New(cfg config) *Cache {
	return &Cache{client: goredis.NewClient(&goredis.Options{Addr: cfg.addr})}
}
```

Why a fixed TTL instead of something more sophisticated? Because an order, once created, never
changes in this service — there's no "update order" endpoint — which makes the caching problem
almost trivially easy. A short TTL just bounds how long a theoretically-stale read could last, and
there's no code path that writes to Postgres without also being able to update the cache in the
same request (you'll see that in `CreateOrder`, next chapter). A service where orders *can* change
after creation needs either active invalidation on every write path, or has to accept
eventually-stale reads as a deliberate trade-off — see
[chapter 21](21-alternatives-and-further-reading.md#caching) for what changes once that's true.

Add the same three capability methods you wrote for `postgres.Store` — the reasoning is identical,
so it's worth noticing the pattern is already starting to feel familiar:

```go
func (c *Cache) Init(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *Cache) Stop(context.Context) error {
	return c.client.Close()
}

func (c *Cache) Health(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}
```

Now `Get`, where the miss-versus-error decision from earlier actually gets used:

```go
func (c *Cache) Get(ctx context.Context, id uuid.UUID) (*domain.Order, error) {
	raw, err := c.client.Get(ctx, key(id)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, cache.ErrMiss
	}
	if err != nil {
		return nil, fmt.Errorf("redis: get: %w", err)
	}

	var o domain.Order
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("redis: unmarshal: %w", err)
	}
	return &o, nil
}
```

`goredis.Nil` is go-redis's own sentinel for "this key doesn't exist" — translating it into
`cache.ErrMiss` right here is the same move you made for `pgx.ErrNoRows` in the last chapter.
Nothing above this package should ever need to import `go-redis` just to check an error. `Set` and
`Invalidate` finish the interface:

```go
func (c *Cache) Set(ctx context.Context, o *domain.Order) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("redis: marshal: %w", err)
	}
	if err := c.client.Set(ctx, key(o.ID), raw, ttl).Err(); err != nil {
		return fmt.Errorf("redis: set: %w", err)
	}
	return nil
}

func (c *Cache) Invalidate(ctx context.Context, id uuid.UUID) error {
	if err := c.client.Del(ctx, key(id)).Err(); err != nil {
		return fmt.Errorf("redis: del: %w", err)
	}
	return nil
}

func key(id uuid.UUID) string {
	return "order:" + id.String()
}
```

## Try it against a real Redis

```
$ make up
$ TEST_REDIS_ADDR=localhost:6379 go test ./redis/... -v
=== RUN   TestGetOnEmptyKeyReturnsErrMiss
--- PASS: TestGetOnEmptyKeyReturnsErrMiss (0.01s)
=== RUN   TestSetThenGetRoundTrips
--- PASS: TestSetThenGetRoundTrips (0.00s)
=== RUN   TestInvalidateRemovesTheKey
--- PASS: TestInvalidateRemovesTheKey (0.00s)
PASS
ok  	example.com/servoorders/internal/cache/redis	0.174s
```

(`make test-integration` runs this alongside Postgres and NATS's own tests together — shown
separately here since this chapter is only about Redis so far.) One thing worth writing a test for
specifically: a `time.Time` round-trips through JSON as RFC 3339 text, which isn't
nanosecond-precise. `TestSetThenGetRoundTrips` truncates to millisecond precision before comparing,
for exactly that reason — don't assume a value that's been through the cache is byte-identical to
the one you put in.

## Diagnostics

- **`redis: get: dial tcp ...: connect: connection refused`** — Redis isn't running, or isn't on
  the address `REDIS_ADDR` names. `make up` followed by `docker ps` confirms it's actually up;
  `redis-cli -h localhost ping` answering `PONG` is the fastest independent check.
- **Stale data after a value should have changed** — if this service ever grows an update path,
  this is the first place to look: an update that writes to Postgres but forgets to also call `Set`
  or `Invalidate` leaves the cache serving the old value until the TTL expires.

## Do's and don'ts

- **Do** treat every cache error except `ErrMiss` as "log and fall back," never as a request
  failure — a cache is an optimization, and a service that returns 500s when Redis hiccups has
  turned an optional dependency into a required one. You'll see this pattern applied for real in
  [chapter 8](08-service-layer.md).
- **Do** put a TTL on everything. An entry with no expiry that's never explicitly invalidated is
  how a caching bug becomes a "why is this three days stale" incident.
- **Don't** reach for a distributed cache at all until a profiler or a load test says the database
  is actually the bottleneck. Redis here is instructive, not a default every service needs from day
  one.

## Next

[Chapter 7: Messaging layer](07-messaging-layer.md) — NATS, and publishing the event this
service's other consumers care about.
