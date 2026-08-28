//go:build servoinject

package extractor

import "github.com/okian/servo/v3/servo"

func wire() {
	servo.Build(
		servo.Root[*Server](),
		servo.Scoped[*Session, Sessions](),
	)
}
