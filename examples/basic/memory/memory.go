// Package memory implements store.Store, and nothing in this module imports
// it. That is what it is here to show: servo resolves over the packages
// actually reachable from the injector, so a type no import path leads to is
// never a candidate, however well it satisfies the interface.
//
// Delete the servo.Bind line in cmd/basic/spec.go and servo reports two
// candidates for store.Store — mockstore.Store and postgres.DB. This type is
// not among them.
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
