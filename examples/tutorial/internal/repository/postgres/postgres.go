// Package postgres implements repository.OrderRepository and
// repository.UserRepository against a real Postgres database via pgx. One
// Store satisfies both interfaces since, at this service's size, splitting
// them into separate structs would only add indirection — see
// docs/tutorial/05-repository-layer.md for when that split earns its keep.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"example.com/servoorders/internal/domain"
	"example.com/servoorders/internal/repository"
	"example.com/servoorders/internal/repository/migrations"
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

// Config is this package's own configuration, declared here and nowhere
// else — adding or removing a setting is a one-file change. The directive
// makes servo generate the loader (servo_config_gen.go, beside this file):
// the prefix namespaces the environment variable, so the tag below reads
// POSTGRES_DSN, and a deployment missing it fails at startup with an error
// that names it.
//
//servo:config prefix=POSTGRES
type Config struct {
	DSN string `config:"dsn,required"`
}

func New(cfg Config) (*Store, error) {
	pool, err := pgxpool.New(context.Background(), cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Init runs once, in dependency order, before anything that needs the
// database is allowed to proceed — a connection failure or a broken
// migration surfaces as a startup error, not a request-time 500 the first
// time a handler happens to touch the database.
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

func (s *Store) Create(ctx context.Context, o *domain.Order) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO orders (id, user_id, item, quantity, status, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		o.ID, o.UserID, o.Item, o.Quantity, o.Status, o.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: create order: %w", err)
	}
	return nil
}

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
