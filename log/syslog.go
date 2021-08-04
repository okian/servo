package log

import (
	"log/syslog"

	"go.uber.org/zap/zapcore"

	"github.com/okian/servo/v3/cfg"
)

func newSysLog() (zapcore.WriteSyncer, error) {
	w, err := syslog.Dial(cfg.GetString("log_syslog_network"),
		cfg.GetString("log_syslog_address"),
		syslog.LOG_INFO,
		"")
	return zapcore.AddSync(w), err
}
