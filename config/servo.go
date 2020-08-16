package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/okian/servo"
	"github.com/spf13/viper"
)

const configFile = "config"

type cfg struct{}

func (c *cfg) Name() string {
	return "config"
}

func (c *cfg) Initialize(_ context.Context) error {
	viper.SetEnvPrefix(AppName())
	viper.AddConfigPath(fmt.Sprintf("/etc/%s/", AppName()))
	viper.AddConfigPath(fmt.Sprintf("$HOME/.%s/", AppName()))
	viper.AddConfigPath(".")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	viper.AutomaticEnv()
	_ = viper.ReadInConfig()
	return nil
}

func (c *cfg) Finalize() error {
	return nil
}

func (c *cfg) Healthy(_ context.Context) (interface{}, error) {
	return nil, nil
}

func (c *cfg) Ready(_ context.Context) (interface{}, error) {
	return nil, nil
}

func init() {
	c := &cfg{}
	servo.Register(c, 10)
}
