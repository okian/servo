package noscopekey

import "context"

// Cache is declared as a scope in spec.go, but has no ScopeKey method —
// the reverse of `undeclared/`, and the half a new user meets first,
// having reached for servo.Scoped before writing the extractor.
type Cache struct{}

func New() *Cache { return &Cache{} }

func (c *Cache) Stop(ctx context.Context) error { return nil }

// Caches is the accessor interface, written correctly. Only the method on
// Cache itself is missing.
type Caches interface {
	Acquire(ctx context.Context) (*Cache, func(), error)
}

type Server struct{ caches Caches }

func NewServer(caches Caches) *Server { return &Server{caches: caches} }
