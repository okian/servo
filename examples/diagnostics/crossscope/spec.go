//go:build servoinject

package crossscope

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Server](),
		servo.Scoped[*Room, Rooms](),
		servo.Scoped[*Tenant, Tenants](),
	)
}
