// Package migrations applies the embedded *.sql files in filename order,
// once each, tracked in a schema_migrations table — a small hand-rolled
// runner rather than a dependency like golang-migrate, so there's nothing
// here beyond what a reader can read top to bottom in one sitting. See
// docs/tutorial/05-repository-layer.md for when a dedicated tool earns its
// keep instead.
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
