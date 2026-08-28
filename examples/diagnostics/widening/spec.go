//go:build servoinject

package widening

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Server](),
		servo.Scoped[*Room, Rooms](),
	)
}
