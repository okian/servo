package logger

import (
	"context"
	"log"
)

type Logger struct{}

func New() *Logger {
	return &Logger{}
}

func (l *Logger) Printf(format string, args ...any) {
	log.Printf(format, args...)
}

func (l *Logger) Stop(ctx context.Context) error {
	log.Println("logger: flushed")
	return nil
}
