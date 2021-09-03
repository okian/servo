package tracing

import (
	"github.com/okian/servo/v2/lg"
)

type logger struct {
}

func (l *logger) Error(msg string) {
	lg.Error(msg)
}

func (l *logger) Infof(msg string, args ...interface{}) {
	lg.Infof(msg, args)
}
