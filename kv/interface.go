package kv

type Interface interface {
	Set(key string, val interface{}) error
	SetWithTTL(key string, val interface{}, ttl uint64) error
	Get(key string, rcv interface{}) error
	Decr(key string, val int, ttl uint64) (int, error)
	Incr(key string, val int, ttl uint64) (int, error)
}

func Register(i Interface) {

}
