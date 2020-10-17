package kv

import "time"

type Interface interface {
	Set(key string, val interface{}) error
	BitSet(key string, idx int, val bool, ttl time.Duration) error
	BitGet(key string, idx int) (bool, error)
	SetWithTTL(key string, val interface{}, ttl time.Duration) error
	Get(key string, rcv interface{}) error
	Decr(key string, val int, ttl time.Duration) (int, error)
	Incr(key string, val int, ttl time.Duration) (int, error)
	TTL(key string) (time.Duration, error)
	Delete(key string) error
}

var impl Interface

func Register(i Interface) {
	impl = i
}

func BitSet(key string, idx int, val bool, ttl time.Duration) error {
	return impl.BitSet(key, idx, val, ttl)
}

func BitGet(key string, idx int) (bool, error) {
	return impl.BitGet(key, idx)
}

func Set(key string, val interface{}) error {
	return impl.Set(key, val)
}

func SetWithTTL(key string, val interface{}, ttl time.Duration) error {
	return impl.SetWithTTL(key, val, ttl)
}

func Get(key string, rcv interface{}) error {
	return impl.Get(key, rcv)
}

func Decr(key string, val int, ttl time.Duration) (int, error) {
	return impl.Incr(key, -val, ttl)
}

func Incr(key string, val int, ttl time.Duration) (int, error) {
	return impl.Incr(key, val, ttl)
}

func TTL(key string) (time.Duration, error) {
	return impl.TTL(key)
}

func Delete(key string) error {
	return impl.Delete(key)
}
