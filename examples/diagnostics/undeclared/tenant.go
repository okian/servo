// Package undeclared has a type with a ScopeKey method that no
// servo.Scoped declaration names. A ScopeKey method is what makes a type
// keyed rather than a singleton, and servo will not infer the rest of the
// declaration from it: the accessor interface has to be one the user
// names, because servo cannot emit a type into their package.
package undeclared

import (
	"context"

	"github.com/okian/servo/v3/servo"
)

type TenantKey string

type ctxKey struct{}

type Tenant struct{ key TenantKey }

func NewTenant(key TenantKey) *Tenant { return &Tenant{key: key} }

func (*Tenant) ScopeKey(ctx context.Context) (TenantKey, error) {
	k, ok := ctx.Value(ctxKey{}).(TenantKey)
	if !ok {
		return "", servo.ErrNoScopeKey
	}
	return k, nil
}

type Server struct{ tenant *Tenant }

func NewServer(t *Tenant) *Server { return &Server{tenant: t} }
