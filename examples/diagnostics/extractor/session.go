// Package extractor gives a ScopeKey method a dependency that is itself
// scoped. The extractor is what decides which instance a caller gets, so
// it runs before any instance exists — everything it takes has to be
// already constructed.
package extractor

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type SessionKey string

type ctxKey struct{}

type Sessions interface {
	Acquire(ctx context.Context) (*Session, func(), error)
}

// Decoder is scoped: it depends on SessionKey, so there is one per
// session.
type Decoder struct{ key SessionKey }

func NewDecoder(key SessionKey) *Decoder { return &Decoder{key: key} }

type Session struct {
	key SessionKey
	dec *Decoder
}

func NewSession(key SessionKey, d *Decoder) *Session { return &Session{key: key, dec: d} }

// ScopeKey takes *Decoder, which cannot exist yet — choosing the Decoder
// requires the key this method is being called to produce.
func (*Session) ScopeKey(ctx context.Context, d *Decoder) (SessionKey, error) {
	k, ok := ctx.Value(ctxKey{}).(SessionKey)
	if !ok {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

type Server struct{ sessions Sessions }

func NewServer(s Sessions) *Server { return &Server{sessions: s} }
