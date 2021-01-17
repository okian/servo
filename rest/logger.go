package rest

import (
	"github.com/labstack/echo/v4"
	"github.com/okian/servo/v2/lg"
)

func logger(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (err error) {

		err = next(c)
		if err != nil {
			lg.Errorf("%s %s %d Error: %q", c.Request().Method, c.Request().URL.String(), c.Response().Status, err.Error())
			c.Error(err)
			return nil
		}
		lg.Infof("%s %s %d", c.Request().Method, c.Request().URL.String(), c.Response().Status)

		return nil
	}
}
