package monitoring

import (
	"github.com/okian/servo/v3"
)

func init() {
	servo.Register(&service{}, 30)
}
