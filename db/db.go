package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/okian/servo/v2/config"
	"github.com/okian/servo/v2/lg"
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
		config.GetString("db_port"),
		config.GetString("db_user"),
		config.GetString("db_dbname"),
		config.GetString("db_password"))

	if v := config.GetString("db_tz"); v != "" {
		cn = fmt.Sprintf("%s timezone='%s'", cn, v)
	}
	return cn

}

func mysqlCS(host string) string {
	return fmt.Sprintf(config.GetString("db_dsn"), config.GetString("db_password"), host)
}

func connect(ctx context.Context, host string) (d *sqlx.DB, err error) {
	switch dr := config.GetString("db_driver"); {
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
	if m := config.GetInt("db_max_open_connection"); m != 0 {
		d.SetMaxOpenConns(m)
	}
	if m := config.GetInt("db_max_idle_connection"); m != 0 {
		d.SetMaxIdleConns(m)
	}

	if m := config.GetDuration("db_max_connection_lifetime"); m != 0 {
		d.SetConnMaxLifetime(m * time.Second)
	}
	if m := config.GetDuration("db_max_idle_time"); m != 0 {
		d.SetConnMaxIdleTime(m * time.Second)
	}
	if config.GetBool("db_monitoring") {
		go monitor(ctx, d, host)
	}
	if err := d.PingContext(ctx); err != nil {
		err := fmt.Errorf("fail to ping %s", host)
		if config.GetBool("db_required") {
			return nil, err
		}
		lg.Warn(err)
	}

	return d, nil
}

func (s *service) Initialize(ctx context.Context) error {
	if config.GetBool("db_monitoring") {
		metrics()
	}
	if h := config.GetString("db_host"); h != "" {
		db, err := connect(ctx, h)
		if err != nil {
			return err
		}
		wdb = append(wdb, db)
		rdb = append(rdb, db)
		return nil
	}

	if ww := strings.Split(config.GetString("db_masters"), ","); len(ww) > 0 {
		wdb = nil
		for _, s := range ww {
			db, err := connect(ctx, s)

			if err != nil {
				return err
			}
			wdb = append(wdb, db)
		}
	}

	if ss := strings.Split(config.GetString("db_slaves"), ","); len(ss) > 0 {
		rdb = nil
		for _, s := range ss {
			db, err := connect(ctx, s)

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

func conv(tx driver.Tx) connection {
	if tx == nil {
		return &ntx{}
	}
	if c, ok := tx.(connection); ok {
		return c
	}
	panic("bad TX")
}

// Exec executes a query without returning any rows.
// The args are for any placeholder parameters in the query.
func Exec(ctx context.Context, tx driver.Tx, query string, args ...interface{}) (sql.Result, error) {
	f := trace(ctx, query)
	r, err := conv(tx).Exec(ctx, query, args...)
	return r, f(err)
}

// NamedQuery using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedQuery(ctx context.Context, tx driver.Tx, query string, arg interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := conv(tx).NamedQuery(ctx, query, arg)
	return r, f(err)
}

// NamedExec using this db.
// Any named placeholder parameters are replaced with fields from arg.
func NamedExec(ctx context.Context, tx driver.Tx, query string, arg interface{}) (sql.Result, error) {
	f := trace(ctx, query)
	r, err := conv(tx).NamedExec(ctx, query, arg)
	return r, f(err)
}

// Select using this db.
// Any placeholder parameters are replaced with supplied args.
func Select(ctx context.Context, tx driver.Tx, dest interface{}, query string, args ...interface{}) error {
	return trace(ctx, query)(conv(tx).Select(ctx, dest, query, args...))
}

// Get using this db.
// Any placeholder parameters are replaced with supplied args.
// An error is returned if the result set is empty.
func Get(ctx context.Context, tx driver.Tx, dest interface{}, query string, args ...interface{}) error {
	return trace(ctx, query)(conv(tx).Get(ctx, dest, query, args...))
}

// Query queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func Query(ctx context.Context, tx driver.Tx, query string, args ...interface{}) (*sqlx.Rows, error) {
	f := trace(ctx, query)
	r, err := conv(tx).Query(ctx, query, args...)
	return r, f(err)
}

// QueryRow queries the database and returns an *sqlx.Row.
// Any placeholder parameters are replaced with supplied args.
func QueryRow(ctx context.Context, tx driver.Tx, query string, args ...interface{}) *sqlx.Row {
	defer trace(ctx, query)(nil)
	return conv(tx).QueryRow(ctx, query, args...)
}

// Prepare returns an sqlx.Stmt instead of a sql.Stmt
func Prepare(ctx context.Context, tx driver.Tx, query string) (*sqlx.Stmt, error) {
	f := trace(ctx, query)
	r, err := conv(tx).Prepare(ctx, query)
	return r, f(err)
}

// PrepareNamed returns an sqlx.NamedStmt
func PrepareNamed(ctx context.Context, tx driver.Tx, query string) (*sqlx.NamedStmt, error) {
	f := trace(ctx, query)
	r, err := conv(tx).PrepareNamed(ctx, query)
	return r, f(err)
}
