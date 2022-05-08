package mem

import (
	"context"
	"github.com/okian/servo/v2/kv"
	"sync"
)

type service struct {
	data      map[string]obj
	Collector map[string]map[string]struct{}
	sync.RWMutex
}

func (k *service) Finalize() error {
	k.data = make(map[string]obj)

}

func (k *service) Name() string {
	return "mem"
}

func (k *service) Initialize(ctx context.Context) error {
	k.data = make(map[string]obj)
	k.Collector = map[string]map[string]struct{}{}
	go k.collector(ctx)
	kv.Register(k)
	return nil
}
