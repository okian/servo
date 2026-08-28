// Package crossscope declares two scopes and then has a member of one
// depend on a member of the other. That is a nested scope, deliberately
// rejected in this release: one instance per key pair means two reference
// counts and two linger windows with no single owner.
package crossscope

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type (
	TenantKey string
	RoomKey   string
)

type (
	tenantCtxKey struct{}
	roomCtxKey   struct{}
)

type Tenants interface {
	Acquire(ctx context.Context) (*Tenant, func(), error)
}

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Tenant struct{ key TenantKey }

func NewTenant(key TenantKey) *Tenant { return &Tenant{key: key} }

func (*Tenant) ScopeKey(ctx context.Context) (TenantKey, error) {
	k, ok := ctx.Value(tenantCtxKey{}).(TenantKey)
	if !ok {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

// Room is keyed by RoomKey but takes a *Tenant, which is keyed by
// TenantKey. One Room would then belong to two scopes at once.
type Room struct {
	key    RoomKey
	tenant *Tenant
}

func NewRoom(key RoomKey, t *Tenant) *Room { return &Room{key: key, tenant: t} }

func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) {
	k, ok := ctx.Value(roomCtxKey{}).(RoomKey)
	if !ok {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

type Server struct{ rooms Rooms }

func NewServer(r Rooms) *Server { return &Server{rooms: r} }
