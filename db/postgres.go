package db

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

type service struct {
	db *sqlx.DB
}

func (s *service) Name() string {
	return "db"
}

func (s *service) Initialize(ctx context.Context) error {
	// this Pings the database trying to connect, panics on error
	// use sqlx.Open() for sql.Open() semantics
	db, err := sqlx.Connect("postgres", "user=foo s.db.ame=bar sslmode=disable")
	if err != nil {
		return err
	}
	s.db = db
	return nil
}

func (s *service) Finalize() error {
	return s.db.Close()
}

func (s *service) Healthy(ctx context.Context) (interface{}, error) {
	return nil, s.db.Ping()
}

func (s *service) Ready(ctx context.Context) (interface{}, error) {
	return nil, s.db.Ping()
}

// NamedQuery using this s.db.
// Any named placeholder parameters are replaced with fields from arg.
func (s *service) NamedQuery(query string, arg interface{}) (*sqlx.Rows, error) {
	return s.db.NamedQuery(query, arg)

}

// NamedExec using this s.db.
// Any named placeholder parameters are replaced with fields from arg.
func (s *service) NamedExec(query string, arg interface{}) (sql.Result, error) {
	return s.db.NamedExec(query, arg)
}

// Select using this s.db.
// Any placeholder parameters are replaced with supplied args.
func (s *service) Select(dest interface{}, query string, args ...interface{}) error {
	return s.db.Select(dest, query, args...)
}

// Get using this s.db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func (s *service) Get(dest interface{}, query string, args ...interface{}) error {
	return s.db.Get(dest, query, args...)
}

// MustBegin starts a transaction, and panics on error.  Returns an *sqlx.Tx instead
// of an *sql.Tx.
func (s *service) MustBegin() *sqlx.Tx {
	return s.db.MustBegin()
}

// Beginx begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx.
func (s *service) Beginx() (*sqlx.Tx, error) {
	return s.db.Beginx()
}

// Queryx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (s *service) Queryx(query string, args ...interface{}) (*sqlx.Rows, error) {
	return s.db.Queryx(query, args...)
}

// QueryRowx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func (s *service) QueryRowx(query string, args ...interface{}) *sqlx.Row {
	return s.db.QueryRowx(query, args...)
}

// MustExec (panic) runs MustExec using this database.
// Any placeholder parameters are replaced with supplied args.
func (s *service) MustExec(query string, args ...interface{}) sql.Result {
	return s.db.MustExec(query, args...)
}

// Preparex returns an sqlx.Stmt instead of a sql.Stmt
func (s *service) Preparex(query string) (*sqlx.Stmt, error) {
	return s.db.Preparex(query)
}

// PrepareNamed returns an sqlx.NamedStmt
func (s *service) PrepareNamed(query string) (*sqlx.NamedStmt, error) {
	return s.db.PrepareNamed(query)
}
