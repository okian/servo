package zap

import (
	"github.com/okian/servo/v2"
)

func init() {
	s := &log{}
	servo.Register(s, 20)
}
