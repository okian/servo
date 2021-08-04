package log

import (
	"os"

	"go.uber.org/zap/zapcore"

	"github.com/okian/servo/v3/cfg"
)

func fileWriter() (zapcore.WriteSyncer, error) {
	path := cfg.GetString("log_filepath")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	return zapcore.AddSync(f), nil
}
