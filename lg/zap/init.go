package zap

import (
	"github.com/okian/servo"
	"github.com/okian/servo/lg"
)

func init() {
	s := &log{}
	servo.Register(s, 20)
	lg.Register(s)
}
