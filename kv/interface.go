package kv

import (
	"time"
)

type Interface interface {
	BitSet(key string, idx int, val bool, ttl time.Duration) error
	BitSets(key string, val bool, ttl time.Duration, idx ...int) error
	BitGet(key string, idx int) (bool, error)
	Set(key string, val string, ttl time.Duration) error
	Get(key string, rcv *string) error
	MSet(key string, val interface{}, ttl time.Duration) error
	MGet(key string, rcv interface{}) error
	Decr(key string, val int, ttl time.Duration) (int, error)
	Incr(key string, val int, ttl time.Duration) (int, error)
	TTL(key string) (time.Duration, error)
	Delete(key string) error
}

func Register(n string, i Interface) {
	srv.register(n, i)
}

func BitSets(key string, val bool, ttl time.Duration, idx ...int) error {
	return srv.def().BitSets(key, val, ttl, idx...)
}

func BitSet(key string, idx int, val bool, ttl time.Duration) error {
	return srv.def().BitSet(key, idx, val, ttl)
}

func BitGet(key string, idx int) (bool, error) {
	return srv.def().BitGet(key, idx)
}

func Set(key string, val string, ttl time.Duration) error {
	return srv.def().Set(key, val, ttl)
}

func Get(key string, rcv *string) error {
	return srv.def().Get(key, rcv)
}

func MSet(key string, val interface{}, ttl time.Duration) error {
	return srv.def().MSet(key, val, ttl)
}

func MGet(key string, rcv interface{}) error {
	return srv.def().MGet(key, rcv)
}

func Decr(key string, val int, ttl time.Duration) (int, error) {
	return srv.def().Decr(key, val, ttl)
}

func Incr(key string, val int, ttl time.Duration) (int, error) {
	return srv.def().Incr(key, val, ttl)
}

func TTL(key string) (time.Duration, error) {
	return srv.def().TTL(key)
}

func Delete(key string) error {
	return srv.def().Delete(key)
}
