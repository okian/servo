package log

import (
	"github.com/okian/servo/v3"
)

func init() {
	s := &service{}
	servo.Register(s, 20)
}
