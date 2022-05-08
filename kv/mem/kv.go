package mem

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"math/big"
	"time"
)

func New(ctx context.Context) *service {
	s := &service{
		data:      make(map[string]obj),
		Collector: make(map[string]map[string]struct{}),
	}
	go s.collector(ctx)
	return s
}

const collectorKey = "0102150405"

type obj struct {
	TTLKey string
	Kind   kind
	Value  interface{}
}

var empty = struct{}{}

type kind int

const (
	bits kind = iota
	set
	mset
	// atomic int
	aint
)

func boolToUint(b bool) uint {
	if b {
		return 1
	}
	return 0
}
func uintToBool(v uint) bool {
	switch v {
	case 0:
		return false
	case 1:
		return true
	default:
		panic("invalid value")
	}
}

var ErrorTTL = errors.New("invalid ttl")

func (k *service) BitSet(ctx context.Context, key string, idx int, val bool, ttl time.Duration) error {
	if ttl/time.Second < 1 {
		return ErrorTTL
	}
	var b big.Int
	b.SetBit(&b, idx, boolToUint(val))
	k.Lock()
	defer k.Unlock()
	v, ok := k.data[key]
	if !ok {
		if !val {
			return nil
		}
		k.data[key] = obj{
			TTLKey: k.setTTL(ctx, key, ttl),
			Kind:   bits,
			Value:  b,
		}
		return nil
	}

	if v.Kind != bits {
		return errors.New("calling bitset for set is not allowed")
	}

	d := v.Value.(big.Int)
	if val {
		d.Or(&d, &b)
	} else {
		d.Xor(&d, b.Or(&b, &d))
	}

	k.data[key] = obj{
		TTLKey: k.updateTTL(ctx, key, v.TTLKey, ttl),
		Kind:   v.Kind,
		Value:  d,
	}
	return nil
}

func (k *service) BitSets(ctx context.Context, key string, val bool, ttl time.Duration, idx ...int) error {
	if ttl/time.Second < 1 {
		return ErrorTTL
	}
	var b big.Int
	for _, v := range idx {
		b.SetBit(&b, v, boolToUint(val))
	}
	k.Lock()
	defer k.Unlock()
	v, ok := k.data[key]
	if !ok {
		k.data[key] = obj{
			TTLKey: k.setTTL(ctx, key, ttl),
			Kind:   bits,
			Value:  b,
		}
		return nil
	}

	if v.Kind != bits {
		return errors.New("calling bitset is not allowed")
	}
	d := v.Value.(big.Int)
	if val {
		d.Or(&d, &b)
	} else {
		d.Xor(&d, b.Or(&b, &d))
	}
	k.data[key] = obj{
		TTLKey: k.updateTTL(ctx, key, v.TTLKey, ttl),
		Kind:   v.Kind,
		Value:  d,
	}
	return nil
}

func (k *service) BitGet(ctx context.Context, key string, idx int) (bool, error) {
	k.RLock()
	v, ok := k.data[key]
	k.Unlock()
	if !ok {
		return false, nil
	}
	if v.Kind != bits {
		return false, errors.New("calling BitGet for set is not allowed")
	}
	b := v.Value.(big.Int)
	return uintToBool(b.Bit(idx)), nil
}

func (k *service) Set(ctx context.Context, key string, val string, ttl time.Duration) error {
	if ttl/time.Second < 1 {
		return ErrorTTL
	}
	k.Lock()
	defer k.Unlock()
	v, ok := k.data[key]
	if !ok {
		k.data[key] = obj{
			TTLKey: k.setTTL(ctx, key, ttl),
			Kind:   set,
			Value:  val,
		}
		return nil
	}

	if v.Kind != set {
		return errors.New("calling Set for bitset is not allowed")
	}

	k.data[key] = obj{
		TTLKey: k.updateTTL(ctx, key, v.TTLKey, ttl),
		Kind:   v.Kind,
		Value:  val,
	}
	return nil
}

func (k *service) Get(ctx context.Context, key string, rcv *string) error {
	k.RLock()
	v, ok := k.data[key]
	k.RUnlock()
	if !ok {
		return nil
	}
	if v.Kind != set {
		return errors.New("calling Get for bitset is not allowed")
	}
	*rcv = v.Value.(string)
	return nil
}

func (k *service) MSet(ctx context.Context, key string, val interface{}, ttl time.Duration) error {
	if ttl/time.Second < 1 {
		return ErrorTTL
	}
	k.Lock()
	defer k.Unlock()

	v, ok := k.data[key]
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(val); err != nil {
		return err
	}
	if !ok {
		k.data[key] = obj{
			TTLKey: k.setTTL(ctx, key, ttl),
			Kind:   mset,
			Value:  &b,
		}
		return nil
	}

	if v.Kind != mset {
		return errors.New("calling Set for bitset is not allowed")
	}

	k.data[key] = obj{
		TTLKey: k.updateTTL(ctx, key, v.TTLKey, ttl),
		Kind:   v.Kind,
		Value:  &b,
	}
	return nil
}

func (k *service) MGet(ctx context.Context, key string, rcv interface{}) error {
	k.RLock()
	v, ok := k.data[key]
	k.RUnlock()
	if !ok {
		return nil
	}
	if v.Kind != mset {
		return errors.New("calling MGet for Bits is not allowed")
	}
	b := v.Value.(*bytes.Buffer)
	return gob.NewDecoder(b).Decode(rcv)
}

func (k *service) Decr(ctx context.Context, key string, val int, ttl time.Duration) (int, error) {
	return k.Incr(ctx, key, -val, ttl)
}

func (k *service) Incr(ctx context.Context, key string, val int, ttl time.Duration) (int, error) {
	if ttl/time.Second < 1 {
		return 0, ErrorTTL
	}
	k.Lock()
	defer k.Unlock()
	v, ok := k.data[key]
	if !ok {
		k.data[key] = obj{
			TTLKey: k.setTTL(ctx, key, ttl),
			Kind:   aint,
			Value:  val,
		}
		return val, nil
	}

	if v.Kind != aint {
		return 0, errors.New("key type is exists and incompatible")
	}
	r := v.Value.(int) + val
	k.data[key] = obj{
		TTLKey: k.updateTTL(ctx, key, v.TTLKey, ttl),
		Kind:   v.Kind,
		Value:  r,
	}
	return r, nil
}

func (k *service) TTL(ctx context.Context, key string) (time.Duration, error) {
	k.RLock()
	v, ok := k.data[key]
	k.RUnlock()
	if !ok {
		return 0, nil
	}
	t, err := time.Parse(collectorKey, v.TTLKey)
	if err != nil {
		return 0, err
	}
	return time.Until(t) / time.Second, nil
}

func (k *service) Delete(ctx context.Context, key string) error {
	k.Lock()
	defer k.Unlock()
	v, ok := k.data[key]
	if !ok {
		return nil
	}
	delete(k.Collector[v.TTLKey], key)
	delete(k.data, key)
	return nil
}

func (k *service) updateTTL(ctx context.Context, key string, oldTTL string, ttl time.Duration) string {
	if _, ok := k.Collector[oldTTL]; ok {
		delete(k.Collector[oldTTL], key)
	}
	return k.setTTL(ctx, key, ttl)
}

func (k *service) setTTL(ctx context.Context, key string, ttl time.Duration) string {
	y := time.Now().Add(ttl).Truncate(time.Second).Format(collectorKey)
	if _, ok := k.Collector[y]; ok {
		k.Collector[y][key] = empty
	}
	k.Collector[y] = map[string]struct{}{
		key: empty,
	}
	return y
}

func (k *service) collector(ctx context.Context) {
	kc := key(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case key := <-kc:
			k.Lock()
			for key := range k.Collector[key] {
				delete(k.data, key)
			}
			delete(k.Collector, key)
			k.Unlock()
		}
	}
}

func key(ctx context.Context) <-chan string {
	kc := make(chan string, 1000)
	go func() {
		s := time.Until(time.Now().Add(time.Second).Truncate(time.Second))
		time.Sleep(s)
		t := time.Tick(time.Second)
		for {
			select {
			case <-t:
				kc <- time.Now().Truncate(time.Second).Format(collectorKey)
			case <-ctx.Done():
				close(kc)
				return
			}
		}
	}()
	return kc
}
