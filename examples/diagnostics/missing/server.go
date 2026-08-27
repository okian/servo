// Package missing has no type implementing Store, so servo generate fails
// with servo's plainest diagnostic: no provider, no candidates to suggest.
package missing

type Store interface {
	Get(key string) string
}

type Server struct{ store Store }

func NewServer(s Store) *Server { return &Server{store: s} }
