package db

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/jmoiron/sqlx"
	"github.com/okian/servo/v2/lg"
	"github.com/opentracing/opentracing-go/log"
)

func Tx(ctx context.Context, ops *sql.TxOptions) driver.Tx {
	return &tx{
		tr: traceTrans(ctx),
		Tx: getWDB().MustBeginTx(ctx, ops),
	}
}

type tx struct {
	*sqlx.Tx
	tr  func(error, []log.Field) error
	err bool
}

func (t *tx) CommitOrRollback() error {
	if t.err {
		err := t.Rollback()
		if err != nil {
			lg.Error(err)
		}
		return err
	}
	err := t.Commit()
	if err != nil {
		lg.Error(err)
	}
	return err
}

func (t *tx) Commit() error {
	return t.tr(t.Tx.Commit(), nil)
}

func (t *tx) Rollback() error {
	return t.tr(t.Tx.Rollback(), nil)
}

// NamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *tx) NamedQuery(_ context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	r, err := t.Tx.NamedQuery(query, arg)
	if err != nil {
		t.err = true
	}
	return r, err
}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func (t *tx) Exec(_ context.Context, query string, args ...interface{}) (sql.Result, error) {
	r, err := t.Tx.Exec(query, args...)
	if err != nil {
		t.err = true
	}
	return r, err
}

// NamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func (t *tx) NamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	r, err := t.Tx.NamedExec(query, arg)
	if err != nil {
		t.err = true
	}
	return r, err
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func (t *tx) Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	err := t.Tx.Select(dest, query, args...)
	if err != nil {
		t.err = true
	}
	return err
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (t *tx) Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	err := t.Tx.Get(dest, query, args...)
	if err != nil {
		t.err = true
	}
	return err
}

// Query queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *tx) Query(_ context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	r, err := t.Tx.Queryx(query, args...)
	if err != nil {
		t.err = true
	}
	return r, err
}

// QueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (t *tx) QueryRow(_ context.Context, query string, args ...interface{}) *sqlx.Row {
	return t.Tx.QueryRowx(query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func (t *tx) Prepare(_ context.Context, query string) (*sqlx.Stmt, error) {
	r, err := t.Tx.Preparex(query)
	if err != nil {
		t.err = true
	}
	return r, err
}

// PrepareNamed returns an sqlx.NamedStmt
func (t *tx) PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	f := trace(ctx, query)
	r, err := t.Tx.PrepareNamed(query)
	f(err)
	return r, err
}
