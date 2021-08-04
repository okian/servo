package mem

import (
	"context"
	"sync"

	"github.com/okian/servo/v3/kv"
)

type service struct {
	data      map[string]obj
	Collector map[string]map[string]struct{}
	sync.RWMutex
}

func (k *service) Name() string {
	return "mem"
}

func (k *service) Initialize(ctx context.Context) error {
	go k.collector(ctx)
	kv.Register(k.Name(), k)
	return nil
}
