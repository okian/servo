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

func memoryUsage(ctx context.Context) {
	mem := promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: Namespace(),
		Name:      "memory_usage",
	})
	tick := time.Tick(time.Second)
	for {
		select {
		case <-tick:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			mem.Set(float64(bToMb(m.Sys)))
		case <-ctx.Done():
			return
		}
	}
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

func Namespace() string {

	return viper.GetString("monitoring_namespace")

}
