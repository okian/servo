package monitoring

import (
	"strings"

	"github.com/okian/servo/v2"
	"github.com/okian/servo/v2/config"
	"github.com/ory/viper"
)

func init() {
	viper.SetDefault("monitoring_port", snakeCase(config.AppName()))
	servo.Register(&service{}, 100)
}

func snakeCase(s string) string {
	r := strings.NewReplacer("-", "_", ".", "_")
	s = r.Replace(s)
	return strings.ToLower(s)
}
