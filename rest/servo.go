package rest

import (
	"context"
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
	port := viper.GetString(portKey)
	if port == "" {
		port = "9000"
	}
	e := echo.New()
	e.HideBanner = true

	s.e = e

	s.validator()
	s.middlewares()
	s.routes()
	go func() {
		if err := e.Start(":" + port); err != nil && err != http.ErrServerClosed {
			lg.Error(err)
		}
	}()

	return nil

}

func (s *service) Finalize() error {
	return s.e.Shutdown(context.Background())
}
