package cfg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/okian/servo/v3"
)

type cfg struct{}

var vp = viper.New()

func (c *cfg) Name() string {
	return "config"
}

func (c *cfg) Initialize(_ context.Context) error {
	vp.SetEnvPrefix(AppName())
	vp.AddConfigPath(fmt.Sprintf("/etc/%s/", AppName()))
	vp.AddConfigPath(fmt.Sprintf("$HOME/.%s/", AppName()))
	vp.AddConfigPath(".")
	vp.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	vp.AutomaticEnv()
	_ = vp.ReadInConfig()
	if vp.GetString("tz") != "" {
		z, err := time.LoadLocation(vp.GetString("tz"))
		if err != nil {
			return err
		}
		time.Local = z

	}
	return nil
}

func (c *cfg) Finalize() error {
	return nil
}

func init() {
	c := &cfg{}
	servo.Register(c, 10)
}
