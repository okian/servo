package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/okian/servo/v2"
	"github.com/spf13/viper"
)

var config *viper.Viper

type cfg struct {
}

func (c *cfg) Name() string {
	return "config"
}

func (c *cfg) Initialize(_ context.Context) error {
	v := viper.New()
	v.SetEnvPrefix(AppName())
	v.AddConfigPath(fmt.Sprintf("/etc/%s/", AppName()))
	v.AddConfigPath(fmt.Sprintf("$HOME/.%s/", AppName()))
	v.AddConfigPath(".")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
	_ = v.ReadInConfig()
	if v.GetString("tz") != "" {
		z, err := time.LoadLocation(config.GetString("tz"))
		if err != nil {
			return err
		}
		time.Local = z
	}
	config = v
	return nil
}

func (c *cfg) Finalize() error {
	return nil
}

func init() {
	c := &cfg{}
	servo.Register(c, 10)
}
