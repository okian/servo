package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
)

var (
	wdb     []*sqlx.DB
	nextWDB uint32

	rdb     []*sqlx.DB
	nextRDB uint32
)

func getRDB() *sqlx.DB {
	n := atomic.AddUint32(&nextRDB, 1)
	return rdb[(int(n)-1)%len(rdb)]
}

func getWDB() *sqlx.DB {
	n := atomic.AddUint32(&nextWDB, 1)
	return wdb[(int(n)-1)%len(wdb)]
}

type service struct {
}

func (s *service) Name() string {
	return "db"
}

func postgresCS(host string) string {
	cn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s password=%s sslmode=disable ",
		host,
		viper.GetString("db_port"),
		viper.GetString("db_user"),
		viper.GetString("db_dbname"),
		viper.GetString("db_password"))

	if v := viper.GetString("db_tz"); v != "" {
		cn = fmt.Sprintf("%s timezone='%s'", cn, v)
	}
	return cn

}

func mysqlCS(host string) string {
	return fmt.Sprintf(viper.GetString("db_dsn"), viper.GetString("db_password"), host)
}

func connection(ctx context.Context, host string) (d *sqlx.DB, err error) {
	switch dr := viper.GetString("db_driver"); {
	case dr == "postgres":
		d, err = sqlx.Open(dr, postgresCS(host))
	case dr == "mysql":
		d, err = sqlx.Open(dr, mysqlCS(host))
	default:
		return nil, errors.New("unsupported sql driver")
	}

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
	if viper.GetBool("db_monitoring") {
		go monitor(ctx, d, host)
	}
	return d, nil
}

func (s *service) Initialize(ctx context.Context) error {
	if viper.GetBool("db_monitoring") {
		metrics()
	}
	if h := viper.GetString("db_host"); h != "" {
		db, err := connection(ctx, h)
		if err != nil {
			return err
		}
		wdb = append(wdb, db)
		rdb = append(rdb, db)
		return nil
	}

	if ww := strings.Split(viper.GetString("db_masters"), ","); len(ww) > 0 {
		wdb = nil
		for _, s := range ww {
			db, err := connection(ctx, s)

			if err != nil {
				return err
			}
			wdb = append(wdb, db)
		}
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
	for i := range wdb {
		_ = wdb[i].Close()
	}
	return nil
}

func report() []interface{} {
	var res []interface{}
	for i := range rdb {
		res = append(res, rdb[i].Stats())
	}
	for i := range wdb {
		res = append(res, rdb[i].Stats())
	}

	return res
}

func check() error {
	var err error
	for i := range wdb {
		err = rdb[i].Ping()
		if err != nil {
			return err
		}
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
	return getRDB().NamedQueryContext(ctx, query, arg)

}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func Exec(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return getWDB().ExecContext(ctx, query, args...)
}

// WNamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func WNamedQuery(ctx context.Context, query string, arg interface{}) (*sqlx.Rows, error) {
	return getWDB().NamedQueryContext(ctx, query, arg)

}

// WNamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func WNamedExec(ctx context.Context, query string, arg interface{}) (sql.Result, error) {
	return getWDB().NamedExecContext(ctx, query, arg)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func Select(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return getRDB().SelectContext(ctx, dest, query, args...)
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(ctx context.Context, dest interface{}, query string, args ...interface{}) error {
	return getRDB().GetContext(ctx, dest, query, args...)
}

// MustBegin starts a transaction, and panics on error.  Returns an *sqlx.Tx instead
// of an *sql.Tx.
func MustBegin(ctx context.Context, ops *sql.TxOptions) *sqlx.Tx {
	return getWDB().MustBeginTx(ctx, ops)
}

// Begin begins a transaction and returns an *sqlx.Tx instead of an *sql.Tx.
func Begin(ctx context.Context, ops *sql.TxOptions) (*sqlx.Tx, error) {
	return getWDB().BeginTxx(ctx, ops)
}

// WQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func WQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return getWDB().QueryxContext(ctx, query, args...)
}

// WQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func WQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return getWDB().QueryRowxContext(ctx, query, args...)
}

// RQuery queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func RQuery(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error) {
	return getRDB().QueryxContext(ctx, query, args...)
}

// RQueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func RQueryRow(ctx context.Context, query string, args ...interface{}) *sqlx.Row {
	return getRDB().QueryRowxContext(ctx, query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func Prepare(ctx context.Context, query string) (*sqlx.Stmt, error) {
	return getWDB().PreparexContext(ctx, query)
}

// PrepareNamed returns an sqlx.NamedStmt
func PrepareNamed(ctx context.Context, query string) (*sqlx.NamedStmt, error) {
	return getWDB().PrepareNamedContext(ctx, query)
}
