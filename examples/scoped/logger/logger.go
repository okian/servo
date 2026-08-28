// Package logger is an ordinary singleton: one per process, shared by
// every scoped instance rather than rebuilt alongside them.
package logger

import (
	"context"
	"log"
	"sync"
)

type Logger struct {
	mu    sync.Mutex
	lines []string
}

func New() *Logger { return &Logger{} }

func (l *Logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, format)
	log.Printf(format, args...)
}

// Lines is what the tests assert against, since a logger that only writes
// to stderr proves nothing about what the graph did.
func (l *Logger) Lines() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *Logger) Stop(context.Context) error { return nil }
