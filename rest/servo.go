package rest

import (
	"context"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
)

const portKey string = "rest_port"

func (s *service) Name() string {
	return "rest"
}

func (s *service) Initialize(ctx context.Context) error {
	h := viper.GetString("rest_host")
	p := viper.GetString(portKey)
	if p == "" {
		p = "9000"
	}

	if err := checkPort(h, p); err != nil {
		return err
	}

	e := echo.New()
	e.HideBanner = true

	s.e = e

	s.validator()
	s.middlewares()
	s.routes()
	go func() {
		if err := e.Start(net.JoinHostPort(h, p)); err != nil && err != http.ErrServerClosed {
			lg.Error(err)
		}
	}()

	return nil

}

func (s *service) Finalize() error {
	return s.e.Shutdown(context.Background())
}

func checkPort(h, p string) error {
	conn, err := net.Listen("tcp", net.JoinHostPort(h, p))
	if err != nil {
		return err
	}
	defer conn.Close()
	return nil
}
