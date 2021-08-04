package redis

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mediocregopher/radix/v3"

	"github.com/okian/servo/v3/cfg"
	"github.com/okian/servo/v3/kv"
)

type service struct {
	pool *radix.Pool
	pre  string
}

func (k *service) prefix() string {
	return k.pre
}

func (k *service) Name() string {
	return "redis"
}

func (k *service) Initialize(_ context.Context) error {
	p, err := connection()

	if err != nil {
		return err
	}
	k.pool = p
	switch px := cfg.GetString("redis_prefix"); px {
	case "", "app":
		k.pre = regexp.MustCompile("[^A-Z]+").ReplaceAllString(strings.ToUpper(cfg.AppName()+"_"), "_")
	case "none":
	// nothing
	default:
		k.pre = px
	}
	kv.Register(k.Name(), k)
	return k.pool.Do(radix.Cmd(nil, "PING"))
}

func (k *service) Finalize() error {
	return k.pool.Close()
}

func (k *service) Healthy(_ context.Context) (interface{}, error) {
	return nil, k.pool.Do(radix.Cmd(nil, "PING"))
}

func (k *service) Ready(_ context.Context) (interface{}, error) {
	return nil, k.pool.Do(radix.Cmd(nil, "PING"))
}

type pkgError string

func (p pkgError) Error() string {
	return string(p)
}

const (
	ErrorConnectionField pkgError = "redis connection failed"
	ErrorInvalidHost     pkgError = "redis host is invalid"
	ErrorInvalidPort     pkgError = "redis port is invalid"
)

const (
	user = "redis_user"
	pass = "redis_pass"
	db   = "redis_db"
	host = "redis_host"
	port = "redis_port"
)

func connection() (*radix.Pool, error) {
	var opt = []radix.DialOpt{
		radix.DialTimeout(time.Second * 10),
	}
	user := cfg.GetString(user)
	pass := cfg.GetString(pass)
	switch {
	case user != "" && pass != "":
		opt = append(opt, radix.DialAuthUser(user, pass))
	case pass != "":
		opt = append(opt, radix.DialAuthPass(pass))
	}
	if db := cfg.GetInt(db); db != 0 {
		opt = append(opt, radix.DialSelectDB(db))
	}

	host := cfg.GetString(host)
	port := cfg.GetString(port)
	addr := fmt.Sprintf("%s:%s", host, port)
	var connfunc = func(network, addr string) (radix.Conn, error) {
		return radix.Dial(network, addr, opt...)
	}
	return radix.NewPool("tcp", addr, 20, radix.PoolConnFunc(connfunc))
}
