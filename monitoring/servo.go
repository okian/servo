package monitoring

import (
	"context"
	"net"
	"net/http"

	"github.com/okian/servo/v2/lg"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
)

func (s *service) Name() string {
	return "monitoring"
}

func (s *service) Initialize(ctx context.Context) error {
	port := viper.GetString("monitoring_port")
	if port == "" {
		port = "9001"
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	s.server = &http.Server{
		Addr:        ":" + port,
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	// Run server
	go func() {
		lg.Infof("starting monitoring server on :%s", port)
		if err := s.server.ListenAndServe(); err != http.ErrServerClosed {
			lg.Error(err)
		}
	}()

	go memoryUsage(ctx)

	return nil
}

func (s *service) Finalize() error {
	return s.server.Shutdown(context.Background())
}
