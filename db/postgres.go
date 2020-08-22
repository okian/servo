package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
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
	cn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s password=%s sslmode=disable timezone='%s'",
		viper.GetString("db_host"),
		viper.GetString("db_port"),
		viper.GetString("db_user"),
		viper.GetString("db_dbname"),
		viper.GetString("db_password"),
		viper.GetString("db_tz"))

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

// NamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedQuery(query string, arg interface{}) (*sqlx.Rows, error) {
	return db.NamedQuery(query, arg)

}

// NamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedExec(query string, arg interface{}) (sql.Result, error) {
	return db.NamedExec(query, arg)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func Select(dest interface{}, query string, args ...interface{}) error {
	return db.Select(dest, query, args...)
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(dest interface{}, query string, args ...interface{}) error {
	return db.Get(dest, query, args...)
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
func Queryx(query string, args ...interface{}) (*sqlx.Rows, error) {
	return db.Queryx(query, args...)
}

// QueryRowx queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func QueryRowx(query string, args ...interface{}) *sqlx.Row {
	return db.QueryRowx(query, args...)
}

// MustExec (panic) runs MustExec using this database.
// Any placeholder parameters are replaced with supplied args.
func MustExec(query string, args ...interface{}) sql.Result {
	return db.MustExec(query, args...)
}

// Preparex returns an sqlx.Stmt instead of a sql.Stmt
func Preparex(query string) (*sqlx.Stmt, error) {
	return db.Preparex(query)
}

// PrepareNamed returns an sqlx.NamedStmt
func PrepareNamed(query string) (*sqlx.NamedStmt, error) {
	return db.PrepareNamed(query)
}
