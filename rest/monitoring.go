package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/okian/servo/v2/monitoring"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestsCounter *prometheus.CounterVec
	responseTime    *prometheus.HistogramVec
	responseSize    *prometheus.SummaryVec
	requestSize     *prometheus.SummaryVec
)

const (
	subsystem = "rest"
)

func (s *service) Statictis() {
	requestsCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: subsystem,
		Name:      "requests_total",
	}, []string{
		"path",
		"code",
		"method",
	})

	def := []float64{.01, .025, .05, .1, .25, .5, 1, 2.5}
	responseTime = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: subsystem,
		Name:      "response_time",
		Buckets:   def,
	}, []string{
		"path",
		"code",
		"method",
	})

	responseSize = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: subsystem,
		Name:      "response_size",
	}, []string{
		"path",
		"code",
		"method",
	})

	requestSize = promauto.NewSummaryVec(prometheus.SummaryOpts{
		Namespace: monitoring.Namespace(),
		Subsystem: subsystem,
		Name:      "request_size",
	}, []string{
		"path",
		"code",
		"method",
	})

}

func statictis(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) (err error) {
		path := c.Request().URL.Path
		method := c.Request().Method
		start := time.Now()
		reqSize := computeApproximateRequestSize(c.Request())

		if err = next(c); err != nil {
			c.Error(err)
		}
		code := strconv.Itoa(c.Response().Status)
		elapsed := float64(time.Since(start)) / float64(time.Second)
		resSz := float64(c.Response().Size)

		requestsCounter.WithLabelValues(path, code, method).Inc()
		requestSize.WithLabelValues(path, code, method).Observe(float64(reqSize))
		responseSize.WithLabelValues(path, code, method).Observe(resSz)
		responseTime.WithLabelValues(path, code, method).Observe(elapsed)

		return

	}
}

func computeApproximateRequestSize(r *http.Request) int {
	s := 0
	if r.URL != nil {
		s = len(r.URL.Path)
	}

	s += len(r.Method)
	s += len(r.Proto)
	for name, values := range r.Header {
		s += len(name)
		for _, value := range values {
			s += len(value)
		}
	}
	s += len(r.Host)

	// N.B. r.Form and r.MultipartForm are assumed to be included in r.URL.

	if r.ContentLength != -1 {
		s += int(r.ContentLength)
	}
	return s
}