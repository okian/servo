package zap

import (
	"context"
	"strings"

	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
)

func (l *log) Name() string {
	return "zap_logger"
}

func (l *log) Initialize(_ context.Context) error {
	if strings.ToUpper(viper.GetString("environment")) == "PRODUCTION" {
		l.z = l.production()

	} else {
		l.z = l.development()
	}
	lg.Register(l)
	return nil
}

func (l *log) Finalize() error {
	defer l.file.Close()
	err := l.z.Sync()
	if err != nil {
		panic(err.Error())
	}
	return err

}

func (l *log) Healthy(_ context.Context) (interface{}, error) {
	return nil, l.z.Sync()
}

func (l *log) Ready(_ context.Context) (interface{}, error) {
	return nil, l.z.Sync()
}
