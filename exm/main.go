package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/okian/servo/v2"
	_ "github.com/okian/servo/v2/config"
	_ "github.com/okian/servo/v2/kv/redis"
	"github.com/okian/servo/v2/lg"
	_ "github.com/okian/servo/v2/lg"
	_ "github.com/okian/servo/v2/lg/zap"
	"github.com/okian/servo/v2/rest"
	_ "github.com/okian/servo/v2/rest"
)

func main() {
	ctx, cl := context.WithCancel(context.Background())
	defer cl()
	defer servo.Initialize(ctx)()
	<-time.After(time.Hour)
}
func init() {
	r := echo.Map{
		"name": 22,
		"ffff": "dfsdfsf",
	}
	rest.EchoGet("/j", func(c echo.Context) error {
		lg.Info(1)
		json.NewEncoder(c.Response().Writer).Encode(r)
		lg.Info(3)
		return nil
	})
	rest.EchoGet("/", func(c echo.Context) error {
		lg.Info(1)
		c.JSON(http.StatusOK, r)

		//fmt.Fprintf(c.Response().Writer, "%d", 2)
		lg.Info(3)
		return nil
	})
	rest.EchoGet("/r", func(c echo.Context) error {
		lg.Info(1)

		//fmt.Fprintf(c.Response().Writer, "%d", 2)
		lg.Info(3)
		return errors.New("ss")
	})
}
