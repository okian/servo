package redis

import (
	"errors"
	"strconv"

	"github.com/mediocregopher/radix/v3"
)

func Set(key string, val interface{}) error {
	return pool.Do(radix.FlatCmd(nil, "HMSET", key, val))
}

func SetWithTTL(key string, val interface{}, ttl uint64) error {
	if ttl < 1 {
		return errors.New("invalid ttl")
	}

	return pool.Do(radix.Pipeline(
		radix.FlatCmd(nil, "HMSET", key, val),
		radix.Cmd(nil, "EXPIRE", key, strconv.FormatUint(ttl, 10))))
}

func Get(key string, rcv interface{}) error {
	return pool.Do(radix.FlatCmd(rcv, "HMSET", key))
}

func Decr(key string, val int, ttl uint64) (int, error) {
	return Incr(key, -val, ttl)
}

func Incr(key string, val int, ttl uint64) (int, error) {
	var res int
	var err error
	switch val {
	case 0:
		err = pool.Do(radix.Cmd(&res, "INCR", key))
	default:
		err = pool.Do(radix.Cmd(&res, "INCRBY", key, strconv.Itoa(val)))
	}
	if err != nil {
		return 0, err
	}
	if res == val && ttl != 0 {
		err = pool.Do(radix.Cmd(nil, "EXPIRE", key, strconv.FormatUint(ttl, 10)))
	}
	return res, err
}
