package postgres

import (
	"context"

	"example.com/servobasic/logger"
)

type DB struct {
	log *logger.Logger
}

func New(log *logger.Logger) (*DB, error) {
	return &DB{log: log}, nil
}

func (d *DB) Get(key string) string {
	return "value-for-" + key
}

func (d *DB) Init(ctx context.Context) error {
	d.log.Printf("postgres: connected")
	return nil
}

func (d *DB) Stop(ctx context.Context) error {
	d.log.Printf("postgres: disconnected")
	return nil
}

func (d *DB) Health(ctx context.Context) error {
	return nil
}
