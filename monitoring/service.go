package monitoring

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/viper"
)

type service struct {
	server *http.Server
}

func Namespace() string {
	return viper.GetString("monitoring_namespace")

}

func memoryUsage(ctx context.Context) {
	mem := promauto.NewSummary(prometheus.SummaryOpts{
		Namespace: Namespace(),
		Name:      "memory_usage",
	})
	tick := time.Tick(time.Minute)
	for {
		select {
		case <-tick:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			mem.Observe(float64(m.Sys))
		case <-ctx.Done():
			return
		}
	}
}
