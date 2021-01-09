package db

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/okian/servo/v2/monitoring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricOpenConn  *prometheus.GaugeVec
	metricInuseConn *prometheus.GaugeVec
	metricIdleConn  *prometheus.GaugeVec
	metricTotalWait *prometheus.GaugeVec
)

func metrics() {
	metricOpenConn = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: "db",
		Name:      "open_conn",
	}, []string{
		"host",
	})
	metricInuseConn = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: "db",
		Name:      "inuse_conn",
	}, []string{
		"host",
	})
	metricIdleConn = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: "db",
		Name:      "idle_conn",
	}, []string{
		"host",
	})
	metricTotalWait = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: monitoring.Namespace(),
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
			metricOpenConn.WithLabelValues(host).Sub(float64(st.OpenConnections))
			metricInuseConn.WithLabelValues(host).Sub(float64(st.InUse))
			metricIdleConn.WithLabelValues(host).Sub(float64(st.Idle))
			metricTotalWait.WithLabelValues(host).Sub(float64(st.WaitDuration.Milliseconds()))
		case <-ctx.Done():
			return
		}
	}
}
