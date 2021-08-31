package jaeger

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
)

/*
Package jaegertracing provides middleware to Opentracing using Jaeger.

Example:
```
package main
import (
    "github.com/labstack/echo-contrib/jaegertracing"
    "github.com/labstack/echo/v4"
)
func main() {
    e := echo.New()
    // Enable tracing middleware
    c := jaegertracing.New(e, nil)
    defer c.Close()

    e.Logger.Fatal(e.Start(":1323"))
}
```
*/

const defaultComponentName = "echo/v4"

type (
	// TraceConfig defines the config for Trace middleware.
	TraceConfig struct {
		// Skipper defines a function to skip middleware.
		Skipper middleware.Skipper

		// OpenTracing Tracer instance which should be got before
		Tracer opentracing.Tracer

		// ComponentName used for describing the tracing component name
		ComponentName string

		// add req body & resp body to tracing tags
		IsBodyDump bool
	}
)

var (
	// DefaultTraceConfig is the default Trace middleware config.
	DefaultTraceConfig = TraceConfig{
		Skipper:       middleware.DefaultSkipper,
		ComponentName: defaultComponentName,
		IsBodyDump:    false,
	}
)

// Trace returns a Trace middleware.
// Trace middleware traces http requests and reporting errors.
func Trace(tracer opentracing.Tracer) echo.MiddlewareFunc {
	c := DefaultTraceConfig
	c.Tracer = tracer
	c.ComponentName = defaultComponentName
	return TraceWithConfig(c)
}

// TraceWithConfig returns a Trace middleware with config.
// See: `Trace()`.
func TraceWithConfig(config TraceConfig) echo.MiddlewareFunc {
	if config.Tracer == nil {
		panic("echo: trace middleware requires opentracing tracer")
	}
	if config.Skipper == nil {
		config.Skipper = middleware.DefaultSkipper
	}
	if config.ComponentName == "" {
		config.ComponentName = defaultComponentName
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if config.Skipper(c) {
				return next(c)
			}

			req := c.Request()
			opname := "HTTP " + req.Method + " URL: " + c.Path()
			var sp opentracing.Span
			tr := config.Tracer
			if ctx, err := tr.Extract(opentracing.HTTPHeaders,
				opentracing.HTTPHeadersCarrier(req.Header)); err != nil {
				sp = tr.StartSpan(opname)
			} else {
				sp = tr.StartSpan(opname, ext.RPCServerOption(ctx))
			}

			ext.HTTPMethod.Set(sp, req.Method)
			ext.HTTPUrl.Set(sp, req.URL.String())
			ext.Component.Set(sp, config.ComponentName)
			req = req.WithContext(opentracing.ContextWithSpan(req.Context(), sp))
			c.SetRequest(req)

			var err error
			defer func() {
				committed := c.Response().Committed
				status := c.Response().Status

				if err != nil {
					var httpError *echo.HTTPError
					if errors.As(err, &httpError) {
						if httpError.Code != 0 {
							status = httpError.Code
						}
						sp.SetTag("error.message", httpError.Message)
					} else {
						sp.SetTag("error.message", err.Error())
					}
					if status == http.StatusOK {
						// this is ugly workaround for cases when httpError.code == 0 or error was not httpError and status
						// in request was 200 (OK). In these cases replace status with something that represents an error
						// it could be that error handlers or middlewares up in chain will output different status code to
						// client. but at least we send something better than 200 to jaeger
						status = http.StatusInternalServerError
					}
				}
				ext.HTTPStatusCode.Set(sp, uint16(status))
				if status >= http.StatusInternalServerError || !committed {
					ext.Error.Set(sp, true)
				}
				sp.Finish()
			}()
			err = next(c)
			return err
		}
	}
}
