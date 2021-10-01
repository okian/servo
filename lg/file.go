package lg

import (
	"os"

	"github.com/okian/servo/v2/config"
	"go.uber.org/zap/zapcore"
)

func fileWriter() (zapcore.WriteSyncer, error) {
	path := config.GetString("log_filepath")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	return zapcore.AddSync(f), nil
}
