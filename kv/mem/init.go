package mem

import (
	"github.com/okian/servo/v3"
)

func init() {
	servo.Register(&service{}, 20)
}
