package redis

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mediocregopher/radix/v3"
)

func (k *service) Set(key string, val string, ttl time.Duration) error {
	if ttl < time.Second {
		return errors.New("invalid ttl")
	}
	return k.pool.Do(radix.Pipeline(
		radix.Cmd(nil, "SET", k.pre+key, val),
		radix.Cmd(nil, "EXPIRE", k.pre+key, strconv.FormatInt(int64(ttl/time.Second), 10))))
}

func (k *service) Get(key string, rcv *string) error {
	return k.pool.Do(radix.Cmd(rcv, "GET", k.pre+key))
}

func (k *service) MSet(key string, val interface{}, ttl time.Duration) error {
	if ttl < time.Second {
		return errors.New("invalid ttl")
	}
	return k.pool.Do(radix.Pipeline(
		radix.FlatCmd(nil, "HMSET", k.pre+key, val),
		radix.Cmd(nil, "EXPIRE", k.pre+key, strconv.FormatInt(int64(ttl/time.Second), 10))))
}

func (k *service) MGet(key string, rcv interface{}) error {
	return k.pool.Do(radix.FlatCmd(rcv, "HGETALL", k.pre+key))
}

func (k *service) BitSet(key string, idx int, val bool, ttl time.Duration) error {
	if ttl < time.Second {
		return errors.New("invalid ttl")
	}
	var i int
	if val {
		i = 1
	}
	return k.pool.Do(radix.Pipeline(
		radix.FlatCmd(nil, "SETBIT", k.pre+key, fmt.Sprint(idx), i),
		radix.Cmd(nil, "EXPIRE", k.pre+key, strconv.FormatInt(int64(ttl/time.Second), 10))))
}

func (k *service) BitSets(key string, val bool, ttl time.Duration, idx ...int) error {
	if ttl < time.Second {
		return errors.New("invalid ttl")
	}
	var i int
	if val {
		i = 1
	}
	var plp = make([]radix.CmdAction, 0, len(idx)+1)
	for _, v := range idx {
		plp = append(plp, radix.FlatCmd(nil, "SETBIT", k.pre+key, fmt.Sprint(v), i))
	}
	plp = append(plp, radix.Cmd(nil, "EXPIRE", k.pre+key, strconv.FormatInt(int64(ttl/time.Second), 10)))
	return k.pool.Do(radix.Pipeline(plp...))
}

func (k *service) BitGet(key string, idx int) (bool, error) {
	var val int
	err := k.pool.Do(radix.Cmd(&val, "GETBIT", k.pre+key, fmt.Sprint(idx)))
	return val == 1, err
}

func (k *service) Decr(key string, val int, ttl time.Duration) (int, error) {
	return k.Incr(key, -val, ttl)
}

func (k *service) Incr(key string, val int, ttl time.Duration) (int, error) {
	if ttl != 0 && ttl < time.Second {
		return 0, errors.New("invalid ttl")
	}
	var res int
	var err error
	switch val {
	case 0, 1:
		err = k.pool.Do(radix.Cmd(&res, "INCR", k.pre+key))
	default:
		err = k.pool.Do(radix.Cmd(&res, "INCRBY", k.pre+key, strconv.Itoa(val)))
	}
	if err != nil {
		return 0, err
	}
	if ttl != 0 {
		err = k.pool.Do(radix.Cmd(nil, "EXPIRE", k.pre+key,
			strconv.FormatInt(int64(ttl/time.Second), 10)))
	}
	return res, err
}

func (k *service) TTL(key string) (time.Duration, error) {
	var res int
	err := k.pool.Do(radix.Cmd(&res, "TTL", k.pre+key))
	return time.Second * time.Duration(res), err
}

func (k *service) Delete(key string) error {
	return k.pool.Do(radix.Cmd(nil, "DEL", k.pre+key))
}
