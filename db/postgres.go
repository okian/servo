package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
)

var (
	wdb  *sqlx.DB
	rdb  []*sqlx.DB
	next uint32
)

func get() *sqlx.DB {
	n := atomic.AddUint32(&next, 1)
	return rdb[(int(n)-1)%len(rdb)]
}

type service struct {
}

func (s *service) Name() string {
	return "db"
}

func connection(ctx context.Context, host string) (*sqlx.DB, error) {
	// this Pings the database trying to connect, panics on error
	// use sqlx.Open() for sql.Open() semantics
	cn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s password=%s sslmode=disable ",
		host,
		viper.GetString("db_port"),
		viper.GetString("db_user"),
		viper.GetString("db_dbname"),
		strings.Repeat("*", len(viper.GetString("db_password"))))

	cn = fmt.Sprintf("%s timezone='%s'", cn, viper.GetString("db_tz"))

	lg.Debugf("db connection string: %s", cn)

	d, err := sqlx.Open("postgres", cn)
	if err != nil {
		return nil, err
	}
	if err := d.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("fail to ping %s", host)
	}
	if m := viper.GetInt("db_max_open_connection"); m != 0 {
		d.SetMaxOpenConns(m)
	}
	if m := viper.GetInt("db_max_idle_connection"); m != 0 {
		d.SetMaxIdleConns(m)
	}

	if m := viper.GetDuration("db_max_connection_lifetime"); m != 0 {
		d.SetConnMaxLifetime(m * time.Second)
	}
	if m := viper.GetDuration("db_max_idle_time"); m != 0 {
		d.SetConnMaxIdleTime(m * time.Second)
	}
	return d, nil
}

func (s *service) Initialize(ctx context.Context) error {
	if h := viper.GetString("db_host"); h != "" {
		db, err := connection(ctx, h)
		if err != nil {
			return err
		}
		wdb = db
		rdb = append(rdb, wdb)
		return nil
	}

	if h := viper.GetString("db_master"); h != "" {
		db, err := connection(ctx, h)
		if err != nil {
			return err
		}
		wdb = db
		rdb = append(rdb, wdb)
	}

	if ss := strings.Split(viper.GetString("db_slaves"), ","); len(ss) > 0 {
		rdb = nil
		for _, s := range ss {
			db, err := connection(ctx, s)

			if err != nil {
				return err
			}
			rdb = append(rdb, db)
		}
		return nil
	}

	lg.Warn("found master but there is no slave! using master as slave too")
	return nil
}

func (s *service) Finalize() error {
	for i := range rdb {
		_ = rdb[i].Close()
	}
	if wdb != nil {
		_ = wdb.Close()
	}
	return nil
}

func report() []interface{} {
	var res []interface{}
	res = append(res, wdb.Stats())
	for i := range rdb {
		res = append(res, rdb[i].Stats())
	}
	return res
}

func check() error {
	var err error
	err = wdb.Ping()
	if err != nil {
		return err
	}
	for i := range rdb {
		err = rdb[i].Ping()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *service) Healthy(ctx context.Context) (interface{}, error) {
	return report(), check()
}

func (s *service) Ready(ctx context.Context) (interface{}, error) {
	return report(), check()
}

// RNamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func RNamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	return get().NamedQueryContext(ctx, query, arg)

}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return wdb.ExecContext(ctx, query, args...)
}

// WNamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func WNamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	return wdb.NamedQueryContext(ctx, query, arg)

}

// WNamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func WNamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	return wdb.NamedExecContext(ctx, query, arg)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return get().SelectContext(ctx, dest, query, args...)
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return get().GetContext(ctx, dest, query, args...)
}

// MustBegin starts a transaction, and panics on error.  Returns an *sqlx.Tx instead
// of an *sql.Tx.
func MustBegin(ctx context.Context, ops *sql.TxOptions) *sqlx.Tx {
	return wdb.MustBeginTx(ctx, ops)
}

// Begin begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx.
func Begin(ctx context.Context, ops *sql.TxOptions) (*sqlx.Tx, error) {
	return wdb.BeginTxx(ctx, ops)
}

// WQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func WQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return wdb.QueryxContext(ctx, query, args...)
}

// WQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func WQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return wdb.QueryRowxContext(ctx, query, args...)
}

// RQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func RQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return get().QueryxContext(ctx, query, args...)
}

// RQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func RQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return get().QueryRowxContext(ctx, query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func Prepare(ctx context.Context, query string) (*sqlx.Stmt, error) {
	return wdb.PreparexContext(ctx, query)
}

// PrepareNamed returns an sqlx.NamedStmt
func PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	return wdb.PrepareNamedContext(ctx, query)
}
