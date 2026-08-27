// Package ambiguous has two types implementing Store and no servo.Bind to
// pick one, so servo generate fails listing both as candidates.
package ambiguous

type Store interface {
	Get(key string) string
}

type Postgres struct{}

func NewPostgres() *Postgres { return &Postgres{} }

func (p *Postgres) Get(key string) string { return "" }

type Redis struct{}

func NewRedis() *Redis { return &Redis{} }

func (r *Redis) Get(key string) string { return "" }

type Server struct{ store Store }

func NewServer(s Store) *Server { return &Server{store: s} }
