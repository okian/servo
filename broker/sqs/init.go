package sqs

import (
	"github.com/okian/servo/v2"
)

func init() {
	servo.Register(&service{}, 200)
}
