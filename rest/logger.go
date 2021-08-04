package rest

import (
	"time"

	"github.com/labstack/echo/v4"

	log "github.com/okian/servo/v3/log"
)

func logger(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (err error) {

		start := time.Now()
		if err = next(c); err != nil {
			log.Errorf("%s %s %d %s Error: %q", c.Request().Method,
				c.Request().URL.String(),
				c.Response().Status,
				time.Since(start).String(),
				err.Error())
			c.Error(err)
			return
		}
		log.Infof("%s %s %d %s", c.Request().Method, c.Request().URL.String(), c.Response().Status, time.Since(start))

		return
	}
}
