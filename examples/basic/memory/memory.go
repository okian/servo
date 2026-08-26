// Package memory is a second store.Store implementation, existing so the
// example demonstrates a genuinely ambiguous auto-bind: both this and
// postgres.DB satisfy store.Store, so servo.Bind is required (see
// cmd/basic/spec.go). Remove that Bind line to see the ambiguity
// diagnostic servo reports instead.
package memory

import "sync"

type Store struct {
	mu   sync.RWMutex
	data map[string]string
}

func New() *Store {
	return &Store{data: make(map[string]string)}
}

func (s *Store) Get(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key]
}
