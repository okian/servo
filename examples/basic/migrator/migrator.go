package migrator

import (
	"context"

	"example.com/servobasic/logger"
	"example.com/servobasic/postgres"
)

// Target is the schema version to migrate to. It comes from a command-line
// flag, so nothing in the graph can build it — which is what
// servo.Value[migrator.Target]() in cmd/migrator/spec.go declares, and why
// the generated constructor there is NewWith rather than New.
//
// Written as a defined type rather than a bare string for the same reason
// two instances of one type need distinct types: identity in the graph is
// by type, so a second supplied string would be the same node as this one.
type Target string

type Migrator struct {
	db     *postgres.DB
	log    *logger.Logger
	target Target
}

func New(db *postgres.DB, log *logger.Logger, target Target) *Migrator {
	return &Migrator{db: db, log: log, target: target}
}

func (m *Migrator) Init(ctx context.Context) error {
	m.log.Printf("migrator: applied migrations up to %s, schema at %s", m.target, m.db.Get("schema_version"))
	return nil
}
