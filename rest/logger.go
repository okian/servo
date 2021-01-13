package rest

import "github.com/labstack/echo/v4"

func logger(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (err error) {

		if err := next(c); err != nil {
			c.Error(err)
		}
		return nil
	}
}
