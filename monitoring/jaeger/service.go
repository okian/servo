package jaeger

import (
	"io"

	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go/config"
)

var s = &service{}

type service struct {
	samplerType string
	samplerVal  float64
	io.Closer
	opentracing.Tracer
}

// initJaeger returns an instance of Jaeger Tracer that samples 100% of traces and logs all spans to stdout.
func (s *service) initJaeger(service string) error {
	var err error
	cfg := &config.Configuration{
		ServiceName: service,
		Sampler: &config.SamplerConfig{
			Type:  s.samplerType,
			Param: s.samplerVal,
		},
		Reporter: &config.ReporterConfig{
			LogSpans: true,
		},
	}
	cfg, err = cfg.FromEnv()
	if err != nil {
		return err
	}
	s.Tracer, s.Closer, err = cfg.NewTracer(config.Logger(&logger{}))
	opentracing.SetGlobalTracer(s)
	return err
}
