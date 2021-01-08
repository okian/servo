package rest

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
)

const port string = "rest_port"

func (s *service) Name() string {
	return "rest"
}

func (s *service) Initialize(ctx context.Context) error {
	e := echo.New()
	e.HideBanner = true

	s.e = e

	s.validator()
	s.middlewares()
	s.routes()
	go func() {
		if err := e.Start(":" + viper.GetString(port)); err != nil && err != http.ErrServerClosed {
			lg.Error(err)
		}
	}()

	return nil

}

func (s *service) Finalize() error {
	return s.e.Shutdown(context.Background())
}
