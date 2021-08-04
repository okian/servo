package redis

import (
	"github.com/okian/servo/v3"
	"github.com/okian/servo/v3/cfg"
)

func init() {
	cfg.SetDefault(host, "127.0.0.1")
	cfg.SetDefault(port, 6379)
	servo.Register(&service{}, 20)
}
