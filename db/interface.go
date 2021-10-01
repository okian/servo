package db

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jmoiron/sqlx"
)

type connection interface {

	// Exec executes a query without returning any rows.
	// The args are for any placeholder parameters in the query.
	Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error)

	// NamedQuery using this db.
	// Any named placeholder parameters are replaced with fields from arg.
	NamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error)

	// NamedExec using this db.
	// Any named placeholder parameters are replaced with fields from arg.
	NamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error)

	// Select using this db.
	// Any placeholder parameters are replaced with supplied args.
	Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error

	// Get using this db.
	// Any placeholder parameters are replaced with supplied args.
	// An error is returned if the result set is empty.
	Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error

	// Query queries the database and returns an *sqlx.Row.
	// Any placeholder parameters are replaced with supplied args.
	Query(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error)

	// QueryRow queries the database and returns an *sqlx.Row.
	// Any placeholder parameters are replaced with supplied args.
	QueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row

	// Prepare returns an sqlx.Stmt instead of a sql.Stmt
	Prepare(ctx context.Context, query string) (*sqlx.Stmt, error)

	// PrepareNamed returns an sqlx.NamedStmt
	PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error)
}

type CommitOrRollbacker interface {
	driver.Tx
	CommitOrRollback() error
}
