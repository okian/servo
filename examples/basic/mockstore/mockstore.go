// Package mockstore is a hand-written stand-in for what moq/mockery would
// generate for store.Store — servo doesn't ship a mock generator, this is
// just a plain Go type satisfying the interface. It exists only to be
// wired in via servo.Override in a test-only spec, demonstrating classic
// mock semantics: configurable return value, recorded calls.
package mockstore

import (
	"context"
	"sync"
)

type Store struct {
	mu sync.Mutex

	// Return is what Get replies with; configure it before exercising the
	// code under test.
	Return string
	// Gets records every key Get was called with, in order — assert
	// against this after the test runs.
	Gets []string

	// HangOnStop makes Stop ignore its context entirely and block, so a
	// test can exercise the abandoned-node path instead of the clean-stop
	// path.
	HangOnStop bool
}

func New() *Store {
	return &Store{}
}

func (s *Store) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Gets = append(s.Gets, key)
	return s.Return
}

func (s *Store) Stop(ctx context.Context) error {
	if s.HangOnStop {
		// Deliberately not <-ctx.Done(): a component that ignores
		// cancellation and just blocks is exactly what RunStop's budget
		// exists to catch — respecting ctx here would defeat the point.
		select {}
	}
	return nil
}
