package lg

import (
	"log/syslog"

	"github.com/okian/servo/v2/config"
	"go.uber.org/zap/zapcore"
)

func newSysLog() (zapcore.WriteSyncer, error) {
	w, err := syslog.Dial(config.GetString("log_syslog_network"),
		config.GetString("log_syslog_address"),
		syslog.LOG_INFO,
		"")
	return zapcore.AddSync(w), err
}
