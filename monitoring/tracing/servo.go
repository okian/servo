package tracing

import (
	"context"
	"strings"

	"github.com/okian/servo/v2/config"
	"github.com/okian/servo/v2/rest"
	"github.com/opentracing/opentracing-go"
	"github.com/spf13/viper"
)

func (s *service) Name() string {
	return "jaeger"
}

func (s *service) Initialize(ctx context.Context) error {
	var name string
	if v := viper.GetString("JAEGER_SERVICE_NAME"); v != "" {
		name = v
	}
	if name == "" {
		name = strings.ToLower(config.AppName())
	}
	if err := s.initJaeger(name); err != nil {
		return err
	}

	rest.Use(trace(opentracing.GlobalTracer()))
	return nil
}

func (s *service) Finalize() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
