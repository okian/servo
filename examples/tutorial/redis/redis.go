// Package redis implements cache.OrderCache against a real Redis instance.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"example.com/servoorders/cache"
	"example.com/servoorders/config"
	"example.com/servoorders/domain"
	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// ttl is fixed rather than configurable — see
// docs/tutorial/06-caching-layer.md for why a short, fixed TTL is the
// simplest correct answer for a cache with no active invalidation on
// external writes.
const ttl = 5 * time.Minute

type Cache struct {
	client *goredis.Client
}

var _ cache.OrderCache = (*Cache)(nil)

func New(cfg *config.Config) *Cache {
	return &Cache{client: goredis.NewClient(&goredis.Options{Addr: cfg.RedisAddr})}
}

func (c *Cache) Init(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

func (c *Cache) Stop(context.Context) error {
	return c.client.Close()
}

func (c *Cache) Health(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

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
