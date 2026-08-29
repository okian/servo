//go:build prod

// Package postgres is the prod build's store. Note it has a lifecycle the
// in-memory one does not — Init and Stop — so the two configurations do not
// merely swap an implementation, they produce genuinely different graphs.
// That is the case a single generated file cannot serve.
package postgres

import "context"

type DB struct{}

func New() *DB { return &DB{} }

func (d *DB) Init(ctx context.Context) error { return nil }
func (d *DB) Stop(ctx context.Context) error { return nil }

func (d *DB) Get(ctx context.Context, key string) (string, error) {
	return "hello from postgres", nil
}
