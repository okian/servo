package kv

import "time"

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

var impl Interface

func Register(i Interface) {
	if impl != nil {
		panic("multiple call")
	}
	impl = i
}

func BitSets(key string, val bool, ttl time.Duration, idx ...int) error {
	return impl.BitSets(key, val, ttl, idx...)
}

func BitSet(key string, idx int, val bool, ttl time.Duration) error {
	return impl.BitSet(key, idx, val, ttl)
}

func BitGet(key string, idx int) (bool, error) {
	return impl.BitGet(key, idx)
}

func Set(key string, val string, ttl time.Duration) error {
	return impl.Set(key, val, ttl)
}

func Get(key string, rcv *string) error {
	return impl.Get(key, rcv)
}

func MSet(key string, val interface{}, ttl time.Duration) error {
	return impl.MSet(key, val, ttl)
}

func MGet(key string, rcv interface{}) error {
	return impl.MGet(key, rcv)
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
