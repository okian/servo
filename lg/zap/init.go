package zap

import (
	"github.com/okian/servo"
)

func init() {
	s := &log{}
	servo.Register(s, 20)
}
