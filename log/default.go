package log

import (
	l "log"
	"os"
)

func newDefault() Interface {
	return &defaultLogger{
		logger: l.New(os.Stdout, "servo: ", 1|2),
	}

}

type defaultLogger struct {
	logger *l.Logger
}

func (d *defaultLogger) Name() string {
	return "default"
}

func (d *defaultLogger) Info(args ...interface{}) {
	d.logger.Println(args...)
}

func (d *defaultLogger) Debug(args ...interface{}) {
	d.logger.Println(args...)
}

func (d *defaultLogger) Warn(args ...interface{}) {
	d.logger.Println(args...)
}

func (d *defaultLogger) Error(args ...interface{}) {
	d.logger.Println(args...)
}

func (d *defaultLogger) Panic(args ...interface{}) {
	d.logger.Panicln(args...)
}

func (d *defaultLogger) Fatal(args ...interface{}) {
	d.logger.Fatalln(args...)
}

func (d *defaultLogger) Infof(template string, args ...interface{}) {
	d.logger.Printf(template+"\n", args...)
}

func (d *defaultLogger) Debugf(template string, args ...interface{}) {
	d.logger.Printf(template+"\n", args...)
}

func (d *defaultLogger) Warnf(template string, args ...interface{}) {
	d.logger.Printf(template+"\n", args...)
}

func (d *defaultLogger) Errorf(template string, args ...interface{}) {
	d.logger.Printf(template+"\n", args...)
}

func (d *defaultLogger) Panicf(template string, args ...interface{}) {
	d.logger.Panicf(template+"\n", args...)
}

func (d *defaultLogger) Fatalf(template string, args ...interface{}) {
	d.logger.Fatalf(template+"\n", args...)
}
