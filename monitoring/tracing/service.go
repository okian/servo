package tracing

import (
	"context"
	"io"
	"net/http"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"
	"github.com/uber/jaeger-client-go"
	"github.com/uber/jaeger-client-go/config"
	"github.com/uber/jaeger-lib/metrics/prometheus"

	cfg "github.com/okian/servo/v2/config"
)

var s = &service{}

type service struct {
	samplerType string
	samplerVal  float64
	closer      io.Closer
}

func Tag(ctx context.Context, key string, val interface{}) {
	s := opentracing.SpanFromContext(ctx)
	if s != nil {
		s.SetTag(key, val)
	}
}

func tags(s string) []opentracing.Tag {
	var tg []opentracing.Tag
	tg = append(tg, opentracing.Tag{
		Key:   "svc",
		Value: s,
	})
	if cfg.GetBool("tracking_tags_app") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_app",
			Value: cfg.AppName(),
		})
	}

	if cfg.GetBool("tracking_tags_commit") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_commit",
			Value: cfg.Commit(),
		})
	}

	if cfg.GetBool("tracking_tags_date") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_date",
			Value: cfg.Date(),
		})
	}

	if cfg.GetBool("tracking_tags_tag") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_tag",
			Value: cfg.Tag(),
		})
	}

	if cfg.GetBool("tracking_tags_branch") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_branch",
			Value: cfg.Branch(),
		})
	}

	if cfg.GetBool("tracking_tags_version") {
		tg = append(tg, opentracing.Tag{
			Key:   "ext_version",
			Value: cfg.Version(),
		})
	}
	return tg
}

func Inject(ctx context.Context, h *http.Header) {
	sp := opentracing.SpanFromContext(ctx)
	if sp == nil {
		return
	}
	opentracing.GlobalTracer().Inject(sp.Context(), opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(*h))
}

func Trace(ctx context.Context, name string) (context.Context, func(err error, logs ...log.Field) error) {
	sp := opentracing.SpanFromContext(ctx)
	if sp == nil {
		return ctx, func(err error, logs ...log.Field) error {
			return err
		}
	}
	ch := opentracing.StartSpan(name, opentracing.ChildOf(sp.Context()))

	return opentracing.ContextWithSpan(ctx, ch), func(e error, logs ...log.Field) error {
		if e != nil {
			logs = append(logs, log.Error(e))
			ch.SetTag("error", true)
		}
		ch.LogFields(logs...)
		ch.Finish()
		return e
	}
}

// initJaeger returns an instance of Jaeger Tracer that samples 100% of traces and logs all spans to stdout.
func (s *service) initJaeger(service string) error {

	j := config.Configuration{
		ServiceName: service,
		Sampler: &config.SamplerConfig{
			Type:  jaeger.SamplerTypeLowerBound,
			Param: 5,
		},
		Reporter: &config.ReporterConfig{
			User:      cfg.GetString("jaeger_user"),
			Password:  cfg.GetString("jaeger_password"),
			LogSpans:  true,
			QueueSize: 100,
		},
		Tags: tags(service),
	}

	var err error
	_, err = j.FromEnv()
	if err != nil {
		return err
	}
	jMetricsFactory := prometheus.New()
	// Initialize tracer with a logger and a metrics factory
	s.closer, err = j.InitGlobalTracer(service,
		config.Logger(&logger{}),
		config.Metrics(jMetricsFactory),
	)

	return err
}
