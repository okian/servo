// Package realstore is the production implementation of store.Store —
// every example in this module binds it by default; each mock-tool
// subdirectory overrides it in a test-only spec instead.
package realstore

type Store struct{ data map[string]string }

func New() *Store {
	return &Store{data: map[string]string{"user:42": "real-value"}}
}

func (s *Store) Get(key string) string { return s.data[key] }
