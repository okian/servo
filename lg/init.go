package lg

import "github.com/okian/servo"

func init() {
	s := new(in)
	servo.Register(s, 10)
}
