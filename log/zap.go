package log

import (
	"errors"
	"os"
	"strings"

	"go.uber.org/zap/zapcore"

	"github.com/okian/servo/v3/cfg"

	"go.uber.org/zap"
)

type service struct {
	logger *zap.SugaredLogger
}

func logLevel(s string) zapcore.LevelEnabler {
	switch Level(strings.ToUpper(s)) {
	case DebugLevel:
		return zap.DebugLevel
	case InfoLevel:
		return zap.InfoLevel
	case WarnLevel:
		return zap.WarnLevel
	case ErrorLevel:
		return zap.ErrorLevel
	case DPanicLevel:
		return zap.DPanicLevel
	case PanicLevel:
		return zap.PanicLevel
	default:
		return nil
	}
}

func encoder() zapcore.Encoder {
	if strings.ToUpper(cfg.GetString("environment")) == "PRODUCTION" {
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
	if l := logLevel(cfg.GetString("log_file")); l != nil {
		f, err := fileWriter()
		if err != nil {
			return err
		}
		cores = append(cores, zapcore.NewCore(encoder(), f, l))
	}
	if l := logLevel(cfg.GetString("log_syslog")); l != nil {
		w, err := newSysLog()
		if err != nil {
			return err
		}
		cores = append(cores, zapcore.NewCore(encoder(), w, l))
	}

	l := logLevel(cfg.GetString("log_console"))
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
	if cfg.GetBool("service.extra.appname") && cfg.AppName() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_name",
			Type:   zapcore.StringType,
			String: cfg.AppName(),
		})
	}

	if cfg.GetBool("service.extra.branch") && cfg.Branch() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_branch",
			Type:   zapcore.StringType,
			String: cfg.Branch(),
		})
	}

	if cfg.GetBool("service.extra.tag") && cfg.Tag() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_tag",
			Type:   zapcore.StringType,
			String: cfg.Tag(),
		})
	}

	if cfg.GetBool("service.extra.commit") && cfg.Commit() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_commit",
			Type:   zapcore.StringType,
			String: cfg.Commit(),
		})
	}

	if cfg.GetBool("service.extra.version") && cfg.Version() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_version",
			Type:   zapcore.StringType,
			String: cfg.Version(),
		})
	}

	if cfg.GetBool("service.extra.date") && cfg.Date() != "" {
		fs = append(fs, zapcore.Field{
			Key:    "app_date",
			Type:   zapcore.StringType,
			String: cfg.Date(),
		})
	}

	return fs
}
