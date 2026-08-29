// Package store is the interface both configurations wire something into.
package store

import "context"

// Store is the seam the variants differ on: the default build gets an
// in-memory implementation, the prod build a real database.
type Store interface {
	Get(ctx context.Context, key string) (string, error)
}
