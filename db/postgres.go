package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/okian/servo/lg"
	"github.com/spf13/viper"
)

var db *sqlx.DB

type service struct {
}

func (s *service) Name() string {
	return "db"
}

func (s *service) Initialize(ctx context.Context) error {
	// this Pings the database trying to connect, panics on error
	// use sqlx.Open() for sql.Open() semantics
	cn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s password=%s sslmode=disable ",
		viper.GetString("db_host"),
		viper.GetString("db_port"),
		viper.GetString("db_user"),
		viper.GetString("db_dbname"),
		viper.GetString("db_password"))

	cn = fmt.Sprintf("%s timezone='%s'", cn, viper.GetString("db_tz"))

	lg.Debugf("db connection string: %s", cn)

	d, err := sqlx.Connect("postgres", cn)
	if err != nil {
		return err
	}
	db = d
	return nil
}

func (s *service) Finalize() error {
	return db.Close()
}

func (s *service) Healthy(ctx context.Context) (interface{}, error) {
	return nil, db.Ping()
}

func (s *service) Ready(ctx context.Context) (interface{}, error) {
	return nil, db.Ping()
}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return db.ExecContext(ctx, query, arg...)
}

// NamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	return db.NamedQueryContext(ctx, query, arg)

}

// NamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	return db.NamedExecContext(ctx, query, arg)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return db.SelectContext(ctx, dest, query, args...)
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return db.GetContext(ctx, dest, query, args...)
}

// MustBegin starts a transaction, and panics on error.  Returns an *sqlx.Tx instead
// of an *sql.Tx.
func MustBegin() *sqlx.Tx {
	return db.MustBegin()
}

// Beginx begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx.
func Beginx() (*sqlx.Tx, error) {
	return db.Beginx()
}

// Queryx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func Queryx(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return db.QueryxContext(ctx, query, args...)
}

// QueryRowx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func QueryRowx(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return db.QueryRowxContext(ctx, query, args...)
}

// MustExec (panic) runs MustExec using this database.
// Any placeholder parameters are replaced with supplied args.
func MustExec(ctx context.Context, query string, args ...interface{}) sql.Result {
	return db.MustExecContext(ctx, query, args...)
}

// Preparex returns an sqlx.Stmt instead of a sql.Stmt
func Preparex(ctx context.Context, query string) (*sqlx.Stmt, error) {
	return db.PreparexContext(ctx, query)
}

// PrepareNamed returns an sqlx.NamedStmt
func PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	return db.PrepareNamedContext(ctx, query)
}
