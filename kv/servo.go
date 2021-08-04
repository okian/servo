package kv

import (
	"context"
	"errors"
	"sync"

	"github.com/okian/servo/v3"
	"github.com/okian/servo/v3/cfg"
	"github.com/okian/servo/v3/log"
)

var srv = &service{
	impls: make(map[string]Interface),
}

func init() {
	servo.Register(srv, 59)
}

type service struct {
	d     Interface
	impls map[string]Interface
	sync.Mutex
}

const def = "kv_default"

func (s *service) Implements() []string {
	s.Lock()
	defer s.Unlock()
	var im []string
	for k, _ := range s.impls {
		if cfg.GetString(def) == k {
			im = append(im, k+"!")
			continue
		}
		im = append(im, k)
	}
	return im

}

func (s *service) Name() string {
	return "kv"
}

func (s *service) Initialize(ctx context.Context) error {
	s.Lock()
	defer s.Unlock()
	if len(s.impls) == 0 {
		return errors.New("implementation not found")
	}
	if len(s.impls) == 1 {
		for _, v := range s.impls {
			s.d = v
		}
		return nil
	}

	for k, v := range s.impls {
		if cfg.GetString(def) == k {
			s.d = v
		}
	}
	if s.def == nil {
		return errors.New("implementation not found")
	}

	return nil
}

func (s *service) register(n string, i Interface) {
	s.Lock()
	defer s.Unlock()
	if _, ok := s.impls[n]; ok {
		log.Warnf("multiple call for %q", n)
	}
	s.impls[n] = i
}

func (s *service) def() Interface {
	return s.d
}
