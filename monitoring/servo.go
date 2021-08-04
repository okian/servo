package monitoring

import (
	"context"
	"net"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/okian/servo/v3/cfg"
	log "github.com/okian/servo/v3/log"
)

func (s *service) Name() string {
	return "monitoring"
}

func checkPort(h, p string) error {
	conn, err := net.Listen("tcp", net.JoinHostPort(h, p))
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}

func (s *service) Initialize(ctx context.Context) error {
	if !cfg.GetBool("monitoring") {
		return nil
	}
	host := cfg.GetString("monitoring_host")
	port := cfg.GetString("monitoring_port")
	if port == "" {
		port = "9001"
	}

	if err := checkPort(host, port); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	s.server = &http.Server{
		Addr:        net.JoinHostPort(host, port),
		Handler:     mux,
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	// Run server
	go func() {
		log.Infof("starting monitoring server on :%s", port)
		if err := s.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error(err)
		}
	}()

	return nil
}

func (s *service) Finalize() error {
	if s.server != nil {
		return s.server.Shutdown(context.Background())
	}
	return nil
}
