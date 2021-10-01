package rest

import (
	"net/http"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/okian/servo/v2/config"
)

type service struct {
	e *echo.Echo
}

func (s *service) validator() {
	if config.GetBool("rest_validator") {
		if customValidator != nil {
			s.e.Validator = customValidator
			return
		}
		s.e.Validator = &v10Validator{
			validator: validator.New(),
		}
	}
}

func (s *service) middlewares() {
	if config.GetBool("rest_middleware_recover") {
		s.e.Use(middleware.Recover())
	}
	if config.GetBool("rest_middleware_core") {
		s.e.Use(middleware.CORS())
	}
	if config.GetBool("rest_middleware_remove_trailing_slash") {
		s.e.Use(middleware.RemoveTrailingSlash())
	}
	if config.GetBool("rest_middleware_gzip") {
		s.e.Use(middleware.Gzip())
	}
	if config.GetString("rest_middleware_body_limit") != "" {
		s.e.Use(middleware.BodyLimit(config.GetString("rest_middleware_body_limit")))
	}
	if config.GetBool("rest_monitoring") {
		s.Statictis()
		s.e.Use(statictis)
	}

	if config.GetBool("rest_log") {
		s.e.Use(logger)
	}
	s.e.Use(middlewares...)

}

func (s *service) routes() {
	for _, r := range routes {
		switch r.method {
		case http.MethodGet:
			s.e.GET(r.path, r.handler, r.middlewares...)
		case http.MethodPost:
			s.e.POST(r.path, r.handler, r.middlewares...)
		case http.MethodPut:
			s.e.PUT(r.path, r.handler, r.middlewares...)
		case http.MethodPatch:
			s.e.PATCH(r.path, r.handler, r.middlewares...)
		case http.MethodDelete:
			s.e.DELETE(r.path, r.handler, r.middlewares...)
		case http.MethodOptions:
			s.e.OPTIONS(r.path, r.handler, r.middlewares...)
		case http.MethodConnect:
			s.e.CONNECT(r.path, r.handler, r.middlewares...)
		case http.MethodHead:
			s.e.HEAD(r.path, r.handler, r.middlewares...)
		case http.MethodTrace:
			s.e.TRACE(r.path, r.handler, r.middlewares...)
		default:
			panic(r.method)
		}
	}
}
