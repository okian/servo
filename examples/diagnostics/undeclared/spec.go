//go:build servoinject

package undeclared

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Server](),
	)
}
