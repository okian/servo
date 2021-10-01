package db

import (
	"context"
	"time"

	prometheus2 "github.com/okian/servo/v2/monitoring/prometheus"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"

	"github.com/jmoiron/sqlx"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricOpenConn  *prometheus.SummaryVec
	metricInuseConn *prometheus.SummaryVec
	metricIdleConn  *prometheus.SummaryVec
	metricTotalWait *prometheus.SummaryVec
)

func metrics() {
	metricOpenConn = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: prometheus2.Namespace(),
		Subsystem: "db",
		Name:      "open_conn",
	}, []string{
		"host",
	})
	metricInuseConn = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: prometheus2.Namespace(),
		Subsystem: "db",
		Name:      "inuse_conn",
	}, []string{
		"host",
	})
	metricIdleConn = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: prometheus2.Namespace(),
		Subsystem: "db",
		Name:      "idle_conn",
	}, []string{
		"host",
	})
	metricTotalWait = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: prometheus2.Namespace(),
		Subsystem: "db",
		Name:      "total_wait",
	}, []string{
		"host",
	})

}

func monitor(ctx context.Context, d *sqlx.DB, host string) {

	t := time.Tick(time.Second)
	for {
		select {
		case <-t:
			st := d.Stats()
			metricOpenConn.WithLabelValues(host).Observe(float64(st.OpenConnections))
			metricInuseConn.WithLabelValues(host).Observe(float64(st.InUse))
			metricIdleConn.WithLabelValues(host).Observe(float64(st.Idle))
			metricTotalWait.WithLabelValues(host).Observe(float64(st.WaitDuration.Milliseconds()))
		case <-ctx.Done():
			return
		}
	}
}

func trace(ctx context.Context, q string) func(err error) error {
	sp := opentracing.SpanFromContext(ctx)
	if sp == nil {
		return func(err error) error {
			return err
		}
	}
	ch := opentracing.StartSpan("SQL", opentracing.ChildOf(sp.Context()))
	logs := []log.Field{log.String("query", q)}
	return func(e error) error {
		if e != nil {
			logs = append(logs, log.Error(e))
			ch.SetTag("error", true)
		}
		ch.LogFields(logs...)
		ch.Finish()
		return e
	}
}

func traceTrans(ctx context.Context) func(err error, l []log.Field) error {
	sp := opentracing.SpanFromContext(ctx)
	if sp == nil {
		return func(err error, l []log.Field) error {
			return err
		}
	}
	ch := opentracing.StartSpan("SQL Tran", opentracing.ChildOf(sp.Context()))
	return func(e error, l []log.Field) error {
		if e != nil {
			l = append(l, log.Error(e))
			ch.SetTag("error", true)
		}
		ch.LogFields(l...)
		ch.Finish()
		return e
	}
}
