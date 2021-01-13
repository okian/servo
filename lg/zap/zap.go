package zap

import (
	"errors"
	"os"
	"strings"

	"github.com/okian/servo/v2/config"
	"github.com/okian/servo/v2/lg"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"

	"go.uber.org/zap"
)

type service struct {
	logger *zap.SugaredLogger
}

func logLevel(s string) zapcore.LevelEnabler {
	switch lg.Level(strings.ToUpper(s)) {
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
		return nil
	}
}

func encoder() zapcore.Encoder {
	if strings.ToUpper(viper.GetString("environment")) == "PRODUCTION" {
		return zapcore.NewJSONEncoder(zapcore.EncoderConfig{
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
		})
	}
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
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
	})
}

func (s *service) setup() error {

	var cores []zapcore.Core
	if l := logLevel(viper.GetString("log_file")); l != nil {
		f, err := fileWriter()
		if err != nil {
			return err
		}
		cores = append(cores, zapcore.NewCore(encoder(), zapcore.Lock(f), l))
	}
	if l := logLevel(viper.GetString("log_syslog")); l != nil {
		w, err := newSysLog()
		if err != nil {
			return err
		}
		cores = append(cores, zapcore.NewCore(encoder(), w, l))
	}

	l := logLevel(viper.GetString("log_console"))
	if len(cores) == 0 && l == nil {
		l = zap.DebugLevel
	}
	if l != nil {
		cores = append(cores, zapcore.NewCore(encoder(), zapcore.Lock(os.Stderr), l))
	}

	if len(cores) < 1 {
		return errors.New("log config not found")
	}
	s.logger = zap.New(zapcore.NewTee(cores...),
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
		zap.AddCallerSkip(2),
		zap.Fields(extra()...),
	).Sugar()
	return nil
}

func extra() []zapcore.Field {
	var fs []zap.Field
	if viper.GetBool("service.extra.appname") && config.AppName() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_name",
			Type:   zapcore.StringType,
			String: config.AppName(),
		})
	}

	if viper.GetBool("service.extra.branch") && config.Branch() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_branch",
			Type:   zapcore.StringType,
			String: config.Branch(),
		})
	}

	if viper.GetBool("service.extra.tag") && config.Tag() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_tag",
			Type:   zapcore.StringType,
			String: config.Tag(),
		})
	}

	if viper.GetBool("service.extra.commit") && config.Commit() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_commit",
			Type:   zapcore.StringType,
			String: config.Commit(),
		})
	}

	if viper.GetBool("service.extra.version") && config.Version() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_version",
			Type:   zapcore.StringType,
			String: config.Version(),
		})
	}

	if viper.GetBool("service.extra.date") && config.Date() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_date",
			Type:   zapcore.StringType,
			String: config.Date(),
		})
	}

	return fs
}
