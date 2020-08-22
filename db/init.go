package db

import "github.com/okian/servo"

func init() {
	servo.Register(&service{}, 50)
}
