package limiter

import (
	"context"
	"sync"
	"time"
)

type service struct {
	name         string
	ctx          context.Context
	quota        chan bool
	timeout      time.Duration
	rps          uint
	maxQueue     uint
	currentQueue uint
	allow        func(context.Context, *service) bool
	sync.RWMutex
	options []Option
}

func WithUnbalance(t time.Duration) Option {
	return func(s *service) error {
		go func() {
			for {
				<-time.After(time.Second)
				s.Lock()
				s.currentQueue = 0
				s.Unlock()
			}
		}()
		s.timeout = t
		return nil
	}
}

func WithTimeout(t time.Duration) Option {
	return func(s *service) error {
		s.timeout = t
		return nil
	}
}

func WithRPS(i uint) Option {
	return func(s *service) error {
		s.rps = i
		return nil
	}
}

func WithMaxQueue(i uint) Option {
	return func(s *service) error {
		s.maxQueue = i
		return nil
	}
}

func (s *service) calculate() {
	t := time.Tick(time.Second / time.Duration(s.rps))
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t:
			s.quota <- true
		}
	}
}

func (s *service) overflow() bool {
	s.Lock()
	defer s.Unlock()
	s.currentQueue++
	return s.currentQueue > s.maxQueue
}

func allowBalance(ctx context.Context, s *service) bool {
	defer func() {
		s.Lock()
		s.currentQueue--
		s.Unlock()
	}()
	if s.overflow() {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case r := <-s.quota:
		return r
	case <-time.After(s.timeout):
		return false
	}
}

func allowUnbalance(ctx context.Context, s *service) bool {
	s.RLock()
	defer s.RUnlock()
	return s.rps > s.currentQueue
}

func (s *service) Allow(ctx context.Context) bool {
	defer func() {
		s.Lock()
		s.currentQueue--
		s.Unlock()
	}()
	return s.allow(ctx, s)
}

func (s *service) AllowChan(ctx context.Context) <-chan bool {
	r := make(chan bool)
	go func() {
		defer func() {
			close(r)
			s.Lock()
			s.currentQueue--
			s.Unlock()
		}()
		if s.overflow() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case q := <-s.quota:
			r <- q
		case <-time.After(s.timeout):
			return
		}
	}()
	return r
}

type Interface interface {
	Allow(ctx context.Context) bool
}

type Option func(*service) error

func new(name string, ops ...Option) *service {
	return &service{
		name:     name,
		quota:    make(chan bool),
		timeout:  time.Nanosecond,
		rps:      1000,
		maxQueue: 0,
		options:  ops,
	}
}
func NewService(name string, ops ...Option) Interface {
	s := new(name, ops...)
	return s
}

func (s *service) Name() string {
	return s.name + "limiter"
}

func (s *service) Initialize(ctx context.Context) error {
	s.ctx = ctx
	for i := range s.options {
		if err := s.options[i](s); err != nil {
			return err
		}
	}
	go s.calculate()
	return nil
}

func (s *service) Finalize() error {
	return nil
}
