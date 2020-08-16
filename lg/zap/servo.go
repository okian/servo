package zap

import (
	"context"
	"strings"

	"github.com/spf13/viper"
)

type report struct {
	Info  uint `json:"info"`
	Warn  uint `json:"warn"`
	Error uint `json:"error"`
	Debug uint `json:"debug"`
}

func (l *log) Name() string {
	return "zap_logger"
}

func (l *log) Initialize(_ context.Context) error {
	if strings.ToUpper(viper.GetString("environment")) == "PRODUCTION" {
		l.z = l.production()

	} else {
		l.z = l.development()
	}
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
	return check(l)
}

func (l *log) Ready(_ context.Context) (interface{}, error) {
	return check(l)
}
