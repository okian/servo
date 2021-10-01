package db

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type ntx struct {
}

// NamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *ntx) NamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	return getRDB().NamedQueryContext(ctx, query, arg)
}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func (t *ntx) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return getWDB().ExecContext(ctx, query, args...)
}

// NamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *ntx) NamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	return getWDB().NamedExecContext(ctx, query, arg)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func (t *ntx) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return getRDB().SelectContext(ctx, dest, query, args...)
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (t *ntx) Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return getRDB().GetContext(ctx, dest, query, args...)
}

// Query queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *ntx) Query(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return getRDB().QueryxContext(ctx, query, args...)
}

// QueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *ntx) QueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return getRDB().QueryRowxContext(ctx, query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func (t *ntx) Prepare(ctx context.Context, query string) (*sqlx.Stmt, error) {
	return getWDB().PreparexContext(ctx, query)
}

// PrepareNamed returns an sqlx.NamedStmt
func (t *ntx) PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	return getWDB().PrepareNamedContext(ctx, query)
}
