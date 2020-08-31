package redis

import "github.com/okian/servo"

func init() {
	servo.Register(&service{}, 20)
}
