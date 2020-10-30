package zap

import (
	"os"
	"strings"

	"github.com/okian/servo/v2/config"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"

	"go.uber.org/zap"
)

type log struct {
	z    *zap.SugaredLogger
	file *os.File
}

func logLevel() zapcore.Level {
	switch lg.Level(strings.ToUpper(viper.GetString("log.level"))) {
	case lg.DebugLevel:
		return zap.DebugLevel
	case lg.InfoLevel:
		return zap.InfoLevel
	case lg.WarnLevel:
		return zap.WarnLevel
	case lg.ErrorLevel:
		return zap.ErrorLevel
	case lg.DPanicLevel:
		return zap.DPanicLevel
	case lg.PanicLevel:
		return zap.PanicLevel
	default:
		return zap.DebugLevel
	}
}

func writer() *writeSyncer {
	ws := &writeSyncer{}

	if path := viper.GetString("log.filepath"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			panic(err.Error())
		}
		ws.file = f
	}
	return ws
}

func (l *log) development() *zap.SugaredLogger {
	ws := writer()
	if ws.file != nil {
		l.file = ws.file
	}

	core := zapcore.NewCore(zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "ts",
		NameKey:        "data",
		CallerKey:      "caller",
		StacktraceKey:  "stack",
		LineEnding:     "\n--------------------------------------------\n",
		EncodeLevel:    zapcore.CapitalColorLevelEncoder,
		EncodeTime:     nil,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}), ws, logLevel())
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel),
		zap.AddCallerSkip(2)).Sugar()
}

func (l *log) production() *zap.SugaredLogger {
	ws := writer()
	if ws.file != nil {
		l.file = ws.file
	}

	core := zapcore.NewCore(zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		TimeKey:        "ts",
		NameKey:        "data",
		CallerKey:      "caller",
		StacktraceKey:  "stack",
		LineEnding:     "\n",
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
		EncodeName:     zapcore.FullNameEncoder,
	}), ws, logLevel())
	return zap.New(core,
		zap.AddCaller(),
		zap.AddCallerSkip(2),
		zap.Fields(extra()...)).Sugar()

}

func extra() []zapcore.Field {
	fs := []zap.Field{}
	if viper.GetBool("log.extra.appname") && config.AppName() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_name",
			Type:   zapcore.StringType,
			String: config.AppName(),
		})
	}

	if viper.GetBool("log.extra.branch") && config.Branch() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_branch",
			Type:   zapcore.StringType,
			String: config.Branch(),
		})
	}

	if viper.GetBool("log.extra.tag") && config.Tag() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_tag",
			Type:   zapcore.StringType,
			String: config.Tag(),
		})
	}

	if viper.GetBool("log.extra.commit") && config.Commit() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_commit",
			Type:   zapcore.StringType,
			String: config.Commit(),
		})
	}

	if viper.GetBool("log.extra.version") && config.Version() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_version",
			Type:   zapcore.StringType,
			String: config.Version(),
		})
	}

	if viper.GetBool("log.extra.date") && config.Date() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_date",
			Type:   zapcore.StringType,
			String: config.Date(),
		})
	}

	return fs
}
