package redis

import (
	"github.com/okian/servo/v2"
	"github.com/okian/servo/v2/config"
)

func init() {
	config.SetDefault(host, "127.0.0.1")
	config.SetDefault(port, 6379)
	servo.Register(&service{}, 20)
}
