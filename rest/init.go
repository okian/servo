package rest

import (
	"github.com/okian/servo/v2"
	"github.com/ory/viper"
)

func init() {
	viper.SetDefault(port, 9000)
	servo.Register(&service{}, 9999)
}
