package logger

import "log"

type Logger struct {
}

func (l *Logger) Name() string {
	return "default_logger"
}

func (l *Logger) Info(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Debug(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Warn(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Error(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Panic(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Fatal(args ...interface{}) {
	log.Println(args...)
}

func (l *Logger) Infof(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Debugf(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Warnf(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Errorf(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Panicf(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Fatalf(template string, args ...interface{}) {
	log.Print(template)
	log.Println(args...)
}

func (l *Logger) Infow(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}

func (l *Logger) Debugw(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}

func (l *Logger) Warnw(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}

func (l *Logger) Errorw(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}

func (l *Logger) Panicw(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}

func (l *Logger) Fatalw(template string, keysAndValues ...interface{}) {
	log.Print(template)
	log.Println(keysAndValues...)
}
