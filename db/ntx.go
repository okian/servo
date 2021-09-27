package db

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

type query struct {
}

// RNamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *query) RNamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := getRDB().NamedQueryContext(ctx, query, arg)
	return r, f(err)
}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func (t *query) Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	f := trace(ctx, query)
	r, err := getWDB().ExecContext(ctx, query, args...)
	return r, f(err)
}

// WNamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *query) WNamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := getWDB().NamedQueryContext(ctx, query, arg)
	return r, f(err)
}

// WNamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *query) WNamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	f := trace(ctx, query)
	r, err := getWDB().NamedExecContext(ctx, query, arg)
	return r, f(err)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func (t *query) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return trace(ctx, query)(getRDB().SelectContext(ctx, dest, query, args...))
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (t *query) Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return trace(ctx, query)(getRDB().GetContext(ctx, dest, query, args...))
}

// WQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *query) WQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := getWDB().QueryxContext(ctx, query, args...)
	return r, f(err)
}

// WQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *query) WQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	defer trace(ctx, query)(nil)
	return getWDB().QueryRowxContext(ctx, query, args...)
}

// RQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *query) RQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := getRDB().QueryxContext(ctx, query, args...)
	f(err)
	return r, err
}

// RQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *query) RQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	defer trace(ctx, query)(nil)
	return getRDB().QueryRowxContext(ctx, query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func (t *query) Prepare(ctx context.Context, query string) (*sqlx.Stmt, error) {
	f := trace(ctx, query)
	r, err := getWDB().PreparexContext(ctx, query)
	f(err)
	return r, err
}

// PrepareNamed returns an sqlx.NamedStmt
func (t *query) PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	f := trace(ctx, query)
	r, err := getWDB().PrepareNamedContext(ctx, query)
	f(err)
	return r, err
}
