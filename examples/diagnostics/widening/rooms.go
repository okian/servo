// Package widening declares a scope correctly, then has a singleton take
// the scoped type directly instead of the accessor interface. That
// singleton is built once and held for the life of the process, so it
// would capture whichever Room was built first and hand that same one to
// every caller afterwards — the bug this diagnostic exists to catch.
package widening

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type RoomKey string

type ctxKey struct{}

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Room struct{ key RoomKey }

func NewRoom(key RoomKey) *Room { return &Room{key: key} }

func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) {
	k, ok := ctx.Value(ctxKey{}).(RoomKey)
	if !ok {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

// Server takes *Room, not Rooms. That is the mistake.
type Server struct{ room *Room }

func NewServer(r *Room) *Server { return &Server{room: r} }
