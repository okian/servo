package worker

import (
	"context"

	"example.com/servobasic/logger"
)

type Consumer struct {
	log *logger.Logger
}

func New(l *logger.Logger) *Consumer {
	return &Consumer{log: l}
}

func (c *Consumer) Run(ctx context.Context) error {
	c.log.Printf("worker: consuming (Ctrl+C to stop)")
	<-ctx.Done()
	c.log.Printf("worker: run loop exiting")
	return nil
}
