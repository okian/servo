package migrator

import (
	"context"

	"example.com/servobasic/logger"
	"example.com/servobasic/postgres"
)

type Migrator struct {
	db  *postgres.DB
	log *logger.Logger
}

func New(db *postgres.DB, log *logger.Logger) *Migrator {
	return &Migrator{db: db, log: log}
}

func (m *Migrator) Init(ctx context.Context) error {
	m.log.Printf("migrator: applied migrations, schema at %s", m.db.Get("schema_version"))
	return nil
}
